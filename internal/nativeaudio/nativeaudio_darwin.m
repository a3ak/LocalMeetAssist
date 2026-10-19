#import <AppKit/NSRunningApplication.h>
#import <Foundation/Foundation.h>
#import <AudioToolbox/AudioToolbox.h>
#import <CoreAudio/CoreAudio.h>
#import <CoreAudio/AudioHardwareTapping.h>
#import <CoreAudio/CATapDescription.h>

#include <math.h>
#include <pthread.h>
#include <stdint.h>
#include <string.h>
#include <unistd.h>
#include <libproc.h>
#include <stdio.h>
#include <stdlib.h>

void* lm_platform_wav_open(const char* path, unsigned int sample_rate, unsigned int channels);
int lm_platform_wav_write(void* sink, const void* data, size_t bytes);
int lm_platform_wav_close(void* sink);

// lm_macos_device_channel_count reports how many channels a device exposes in
// the given direction; devices without channels are skipped entirely.
static int lm_macos_device_channel_count(AudioObjectID device, AudioObjectPropertyScope scope)
{
    AudioObjectPropertyAddress address = {
        kAudioDevicePropertyStreamConfiguration,
        scope,
        kAudioObjectPropertyElementMain
    };
    UInt32 size = 0;
    if (AudioObjectGetPropertyDataSize(device, &address, 0, NULL, &size) != noErr || size == 0) return 0;
    AudioBufferList* list = (AudioBufferList*)malloc(size);
    if (list == NULL) return 0;
    int channels = 0;
    if (AudioObjectGetPropertyData(device, &address, 0, NULL, &size, list) == noErr) {
        for (UInt32 index = 0; index < list->mNumberBuffers; index++) {
            channels += (int)list->mBuffers[index].mNumberChannels;
        }
    }
    free(list);
    return channels;
}

// lm_macos_any_device_running scans every device in the given direction. The
// default device alone is not enough: a headset or an external interface can
// carry the meeting while the built-in devices stay idle.
static int lm_macos_any_device_running(AudioObjectPropertyScope scope, int* active)
{
    *active = 0;
    AudioObjectPropertyAddress address = {
        kAudioHardwarePropertyDevices,
        kAudioObjectPropertyScopeGlobal,
        kAudioObjectPropertyElementMain
    };
    UInt32 size = 0;
    if (AudioObjectGetPropertyDataSize(kAudioObjectSystemObject, &address, 0, NULL, &size) != noErr || size == 0) return -1;
    int count = (int)(size / sizeof(AudioObjectID));
    AudioObjectID* devices = (AudioObjectID*)calloc((size_t)count, sizeof(AudioObjectID));
    if (devices == NULL) return -1;
    if (AudioObjectGetPropertyData(kAudioObjectSystemObject, &address, 0, NULL, &size, devices) != noErr) {
        free(devices);
        return -1;
    }
    for (int index = 0; index < count; index++) {
        if (lm_macos_device_channel_count(devices[index], scope) <= 0) continue;
        UInt32 running = 0;
        UInt32 running_size = sizeof(running);
        AudioObjectPropertyAddress running_address = {
            kAudioDevicePropertyDeviceIsRunningSomewhere,
            scope,
            kAudioObjectPropertyElementMain
        };
        if (AudioObjectGetPropertyData(devices[index], &running_address, 0, NULL, &running_size, &running) != noErr) continue;
        if (running != 0) {
            *active = 1;
            break;
        }
    }
    free(devices);
    return 0;
}

// lm_macos_any_foreign_process_running_input reports whether some other process
// is capturing the microphone. known stays 0 on macOS versions without the
// per-process API, so the caller can fall back to the device scan.
static int lm_macos_any_foreign_process_running_input(int* active, int* known)
{
    *active = 0;
    *known = 0;
    AudioObjectPropertyAddress address = {
        kAudioHardwarePropertyProcessObjectList,
        kAudioObjectPropertyScopeGlobal,
        kAudioObjectPropertyElementMain
    };
    UInt32 size = 0;
    if (AudioObjectGetPropertyDataSize(kAudioObjectSystemObject, &address, 0, NULL, &size) != noErr || size == 0) return -1;
    int count = (int)(size / sizeof(AudioObjectID));
    AudioObjectID* objects = (AudioObjectID*)calloc((size_t)count, sizeof(AudioObjectID));
    if (objects == NULL) return -1;
    if (AudioObjectGetPropertyData(kAudioObjectSystemObject, &address, 0, NULL, &size, objects) != noErr) {
        free(objects);
        return -1;
    }
    *known = 1;
    for (int index = 0; index < count; index++) {
        UInt32 running = 0;
        UInt32 running_size = sizeof(running);
        AudioObjectPropertyAddress running_address = {
            kAudioProcessPropertyIsRunningInput,
            kAudioObjectPropertyScopeGlobal,
            kAudioObjectPropertyElementMain
        };
        if (AudioObjectGetPropertyData(objects[index], &running_address, 0, NULL, &running_size, &running) != noErr || running == 0) continue;
        pid_t pid = 0;
        UInt32 pid_size = sizeof(pid);
        AudioObjectPropertyAddress pid_address = {
            kAudioProcessPropertyPID,
            kAudioObjectPropertyScopeGlobal,
            kAudioObjectPropertyElementMain
        };
        if (AudioObjectGetPropertyData(objects[index], &pid_address, 0, NULL, &pid_size, &pid) != noErr) continue;
        /* Собственный захват встречей не считается. */
        if (pid == getpid()) continue;
        *active = 1;
        break;
    }
    free(objects);
    return 0;
}

int lm_macos_audio_activity(int* microphone_active, int* system_active, char* error_text, size_t error_size)
{
    int mic_active = 0;
    int mic_known = 0;
    if (lm_macos_any_foreign_process_running_input(&mic_active, &mic_known) != 0) mic_known = 0;
    if (!mic_known && lm_macos_any_device_running(kAudioDevicePropertyScopeInput, &mic_active) != 0) {
        if (error_text != NULL && error_size > 0) snprintf(error_text, error_size, "%s", "CoreAudio activity query failed");
        return -1;
    }
    if (lm_macos_any_device_running(kAudioDevicePropertyScopeOutput, system_active) != 0) {
        if (error_text != NULL && error_size > 0) snprintf(error_text, error_size, "%s", "CoreAudio activity query failed");
        return -1;
    }
    *microphone_active = mic_active;
    return 0;
}

// lm_macos_active_input_app resolves the localized name of the process that is
// actively capturing input, via the per-process CoreAudio objects introduced in
// macOS 14.4. On older systems the query fails cleanly and found stays 0.
int lm_macos_active_input_app(char* name, size_t name_size, int* found, char* error_text, size_t error_size)
{
    (void)error_text;
    (void)error_size;
    if (name == NULL || name_size == 0 || found == NULL) return -1;
    name[0] = '\0';
    *found = 0;

    AudioObjectPropertyAddress address = {
        kAudioHardwarePropertyProcessObjectList,
        kAudioObjectPropertyScopeGlobal,
        kAudioObjectPropertyElementMain
    };
    UInt32 size = 0;
    OSStatus status = AudioObjectGetPropertyDataSize(kAudioObjectSystemObject, &address, 0, NULL, &size);
    if (status != noErr || size == 0) return -1; /* API unavailable (pre-14.4) */

    int count = (int)(size / sizeof(AudioObjectID));
    AudioObjectID* objects = (AudioObjectID*)calloc((size_t)count, sizeof(AudioObjectID));
    if (objects == NULL) return -1;
    status = AudioObjectGetPropertyData(kAudioObjectSystemObject, &address, 0, NULL, &size, objects);
    if (status != noErr) { free(objects); return -1; }

    for (int index = 0; index < count; index++) {
        AudioObjectID object = objects[index];
        UInt32 running = 0;
        UInt32 running_size = sizeof(running);
        AudioObjectPropertyAddress running_address = {
            kAudioProcessPropertyIsRunningInput,
            kAudioObjectPropertyScopeGlobal,
            kAudioObjectPropertyElementMain
        };
        if (AudioObjectGetPropertyData(object, &running_address, 0, NULL, &running_size, &running) != noErr || running == 0) continue;

        pid_t pid = 0;
        UInt32 pid_size = sizeof(pid);
        AudioObjectPropertyAddress pid_address = {
            kAudioProcessPropertyPID,
            kAudioObjectPropertyScopeGlobal,
            kAudioObjectPropertyElementMain
        };
        if (AudioObjectGetPropertyData(object, &pid_address, 0, NULL, &pid_size, &pid) != noErr || pid <= 0) continue;
        /* Себя не показываем: приложение не должно предлагать записать самого себя. */
        if (pid == getpid()) continue;

        const char* label = NULL;
        NSRunningApplication* application = [NSRunningApplication runningApplicationWithProcessIdentifier:pid];
        if (application != nil) {
            if (application.localizedName != nil && application.localizedName.length > 0) label = [application.localizedName UTF8String];
            if (label == NULL && application.bundleIdentifier != nil && application.bundleIdentifier.length > 0) label = [application.bundleIdentifier UTF8String];
        }
        if (label == NULL || label[0] == '\0') {
            // Вспомогательный процесс (например, аудио-хелпер Zoom) может не быть
            // зарегистрированным приложением — тогда берём имя процесса.
            char process_name[512];
            int name_len = proc_name(pid, process_name, sizeof(process_name));
            if (name_len > 0) {
                process_name[sizeof(process_name) - 1] = '\0';
                label = process_name;
            }
        }
        if (label == NULL || label[0] == '\0') continue;
        snprintf(name, name_size, "%s", label);
        *found = 1;
        free(objects);
        return 0;
    }
    free(objects);
    return 0;
}

/*
 * Системный звук на macOS берётся через CoreAudio process taps (macOS 14.2+).
 *
 * Раньше использовался ScreenCaptureKit: сессия захвата экрана, привязанная к
 * конкретному дисплею. При отключении монитора macOS инвалидировала её, и дорожка
 * голосов собеседников обрывалась молча. Тап не зависит от дисплеев вообще: он
 * собирает вывод процессов, а приватное агрегатное устройство превращает его в
 * обычный входной поток CoreAudio.
 */

// Окно фильтра-интерполятора при пересчёте частоты: 16 кГц из 48 или 44,1 кГц.
#define LM_TAP_TAPS 17
#define LM_TAP_MAX_CHANNELS 2
#define LM_TAP_PENDING 8192

typedef struct {
    float pending[LM_TAP_PENDING];
    int pending_frames;
    double phase;
} lm_tap_resampler;

typedef struct {
    AudioObjectID tap_id;
    AudioObjectID aggregate_id;
    AudioDeviceIOProcID proc_id;
    // UID of the tap: the aggregate device refers to it by this string.
    NSString* tap_uid;
    // Set before the teardown so a queued device-change block does not rebuild.
    int stopping;
    AudioObjectPropertyListenerBlock device_listener;
    dispatch_queue_t queue;
    pthread_mutex_t lock;
    void* sink;
    unsigned int channels;
    unsigned int target_rate;
    AudioStreamBasicDescription source_format;
    double step;
    lm_tap_resampler resamplers[LM_TAP_MAX_CHANNELS];
    float* channel_scratch[LM_TAP_MAX_CHANNELS];
    int16_t* output_scratch;
    size_t output_capacity;
    int failed;
    char failure_reason[512];
    uint64_t frames_written;
} lm_macos_capture;

static void lm_macos_capture_fail(lm_macos_capture* capture, const char* message)
{
    if (capture == NULL) return;
    capture->failed = 1;
    if (capture->failure_reason[0] == '\0' && message != NULL) {
        snprintf(capture->failure_reason, sizeof(capture->failure_reason), "%s", message);
    }
}

static void lm_macos_error(char* target, size_t size, NSString* message)
{
    if (target == NULL || size == 0) return;
    snprintf(target, size, "%s", message != nil ? [message UTF8String] : "неизвестная ошибка CoreAudio");
}

static int16_t lm_float_to_s16(float value)
{
    if (value > 1.0f) value = 1.0f;
    if (value < -1.0f) value = -1.0f;
    return (int16_t)lrintf(value < 0 ? value * 32768.0f : value * 32767.0f);
}

static float lm_read_sample(const AudioBufferList* list, const AudioStreamBasicDescription* format, size_t frame, unsigned int channel)
{
    int non_interleaved = (format->mFormatFlags & kAudioFormatFlagIsNonInterleaved) != 0;
    const AudioBuffer* buffer = non_interleaved ? &list->mBuffers[channel] : &list->mBuffers[0];
    size_t index = non_interleaved ? frame : frame * format->mChannelsPerFrame + channel;
    if ((format->mFormatFlags & kAudioFormatFlagIsFloat) != 0 && format->mBitsPerChannel == 32) {
        return ((const float*)buffer->mData)[index];
    }
    if ((format->mFormatFlags & kAudioFormatFlagIsSignedInteger) != 0 && format->mBitsPerChannel == 16) {
        return ((const int16_t*)buffer->mData)[index] / 32768.0f;
    }
    if ((format->mFormatFlags & kAudioFormatFlagIsSignedInteger) != 0 && format->mBitsPerChannel == 32) {
        return ((const int32_t*)buffer->mData)[index] / 2147483648.0f;
    }
    return 0.0f;
}

// lm_tap_sinc returns the normalized interpolation kernel value.
static double lm_tap_sinc(double value)
{
    if (fabs(value) < 1e-9) return 1.0;
    double angle = M_PI * value;
    return sin(angle) / angle;
}

// lm_tap_write resamples the pending buffer and writes it to the WAV sink.
static void lm_tap_flush(lm_macos_capture* capture)
{
    if (capture->failed || capture->sink == NULL) return;
    if (capture->step <= 1.0001) {
        // Частоты совпадают: пересчёт не нужен, только приведение к PCM16.
        while (capture->resamplers[0].pending_frames > 0) {
            int frames = capture->resamplers[0].pending_frames;
            int room = (int)(capture->output_capacity / capture->channels);
            if (frames > room) frames = room;
            for (int frame = 0; frame < frames; frame++) {
                for (unsigned int channel = 0; channel < capture->channels; channel++) {
                    capture->output_scratch[(size_t)frame * capture->channels + channel] =
                        lm_float_to_s16(capture->resamplers[channel].pending[frame]);
                }
            }
            if (lm_platform_wav_write(capture->sink, capture->output_scratch, (size_t)frames * capture->channels * sizeof(int16_t)) != 0) {
                lm_macos_capture_fail(capture, "Ошибка записи системного аудио в WAV");
                return;
            }
            capture->frames_written += (uint64_t)frames;
            for (unsigned int channel = 0; channel < capture->channels; channel++) {
                lm_tap_resampler* resampler = &capture->resamplers[channel];
                memmove(resampler->pending, resampler->pending + frames, sizeof(float) * (size_t)(resampler->pending_frames - frames));
                resampler->pending_frames -= frames;
            }
        }
        return;
    }

    // Общий случай: пересчёт окном sinc. Для каждой выходной точки берём
    // LM_TAP_TAPS входных отсчётов вокруг дробной позиции.
    int produced = 0;
    for (;;) {
        int ready = 1;
        for (unsigned int channel = 0; channel < capture->channels; channel++) {
            lm_tap_resampler* resampler = &capture->resamplers[channel];
            // Окно должно целиком лежать внутри буфера, иначе крайние отсчёты
            // теряются и первые сэмплы получаются с неверной громкостью.
            if (floor(resampler->phase) - LM_TAP_TAPS / 2 + 1 < 0) ready = 0;
            if (resampler->phase + LM_TAP_TAPS > (double)resampler->pending_frames) ready = 0;
        }
        if (!ready) break;
        for (unsigned int channel = 0; channel < capture->channels; channel++) {
            lm_tap_resampler* resampler = &capture->resamplers[channel];
            double position = resampler->phase;
            int first = (int)floor(position) - LM_TAP_TAPS / 2 + 1;
            double weights[LM_TAP_TAPS];
            double total = 0;
            for (int tap = 0; tap < LM_TAP_TAPS; tap++) {
                double distance = (first + tap) - position;
                double t = (double)tap / (double)(LM_TAP_TAPS - 1);
                double window = 0.42 - 0.5 * cos(2.0 * M_PI * t) + 0.08 * cos(4.0 * M_PI * t);
                double weight = lm_tap_sinc(distance * capture->step * 0.98) * window;
                weights[tap] = weight;
                total += weight;
            }
            double mixed = 0;
            for (int tap = 0; tap < LM_TAP_TAPS; tap++) {
                int index = first + tap;
                if (index < 0 || index >= resampler->pending_frames) continue;
                if (total > 1e-9) {
                    mixed += resampler->pending[index] * (weights[tap] / total);
                }
            }
            capture->output_scratch[(size_t)produced * capture->channels + channel] = lm_float_to_s16((float)mixed);
            resampler->phase += capture->step;
        }
        produced++;
        if ((size_t)produced >= capture->output_capacity / capture->channels) break;
    }
    if (produced > 0) {
        if (lm_platform_wav_write(capture->sink, capture->output_scratch, (size_t)produced * capture->channels * sizeof(int16_t)) != 0) {
            lm_macos_capture_fail(capture, "Ошибка записи системного аудио в WAV");
        } else {
            capture->frames_written += (uint64_t)produced;
        }
    }
    // Убираем использованные отсчёты, сохраняя половину окна для следующего раза.
    for (unsigned int channel = 0; channel < capture->channels; channel++) {
        lm_tap_resampler* resampler = &capture->resamplers[channel];
        int consumed = (int)floor(resampler->phase) - LM_TAP_TAPS / 2;
        if (consumed <= 0) continue;
        if (consumed > resampler->pending_frames) consumed = resampler->pending_frames;
        memmove(resampler->pending, resampler->pending + consumed, sizeof(float) * (size_t)(resampler->pending_frames - consumed));
        resampler->pending_frames -= consumed;
        resampler->phase -= consumed;
    }
}

// lm_tap_handle_audio converts one IO callback buffer into the WAV sink.
static void lm_tap_handle_audio(lm_macos_capture* capture, const AudioBufferList* input)
{
    if (capture->failed || capture->sink == NULL || input == NULL) return;
    const AudioStreamBasicDescription* format = &capture->source_format;
    if (format->mChannelsPerFrame == 0) return;
    int non_interleaved = (format->mFormatFlags & kAudioFormatFlagIsNonInterleaved) != 0;
    unsigned int needed_buffers = non_interleaved ? format->mChannelsPerFrame : 1;
    if (input->mNumberBuffers < needed_buffers) {
        lm_macos_capture_fail(capture, "CoreAudio вернул неполный список аудиобуферов");
        return;
    }
    size_t frames = input->mBuffers[0].mDataByteSize / format->mBytesPerFrame;
    if (frames == 0) return;
    for (unsigned int channel = 0; channel < capture->channels; channel++) {
        lm_tap_resampler* resampler = &capture->resamplers[channel];
        float* scratch = capture->channel_scratch[channel];
        unsigned int source_channel = channel < format->mChannelsPerFrame ? channel : format->mChannelsPerFrame - 1;
        for (size_t frame = 0; frame < frames; frame++) {
            float value;
            if (capture->channels == 1 && format->mChannelsPerFrame > 1) {
                float mixed = 0.0f;
                for (unsigned int other = 0; other < format->mChannelsPerFrame; other++) {
                    mixed += lm_read_sample(input, format, frame, other);
                }
                value = mixed / (float)format->mChannelsPerFrame;
            } else {
                value = lm_read_sample(input, format, frame, source_channel);
            }
            scratch[frame] = value;
        }
        int space = LM_TAP_PENDING - resampler->pending_frames;
        if ((int)frames > space) frames = (size_t)space;
        memcpy(resampler->pending + resampler->pending_frames, scratch, sizeof(float) * frames);
        resampler->pending_frames += (int)frames;
    }
    lm_tap_flush(capture);
}

// lm_tap_default_output_uid returns the UID of the current default output device.
static NSString* lm_tap_default_output_uid(void)
{
    AudioObjectPropertyAddress address = {kAudioHardwarePropertyDefaultOutputDevice, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
    AudioObjectID device = kAudioObjectUnknown;
    UInt32 size = sizeof(device);
    if (AudioObjectGetPropertyData(kAudioObjectSystemObject, &address, 0, NULL, &size, &device) != noErr) return nil;
    if (device == kAudioObjectUnknown) return nil;
    AudioObjectPropertyAddress uid_address = {kAudioDevicePropertyDeviceUID, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
    CFStringRef uid = NULL;
    UInt32 uid_size = sizeof(uid);
    if (AudioObjectGetPropertyData(device, &uid_address, 0, NULL, &uid_size, &uid) != noErr || uid == NULL) return nil;
    NSString* result = [(__bridge NSString*)uid copy];
    CFRelease(uid);
    return result;
}

// lm_tap_read_format reads the input format of a device, falling back to the tap.
static int lm_tap_read_aggregate_format(AudioObjectID aggregate, AudioStreamBasicDescription* format)
{
    AudioObjectPropertyAddress address = {kAudioDevicePropertyStreamFormat, kAudioDevicePropertyScopeInput, kAudioObjectPropertyElementMain};
    UInt32 size = sizeof(*format);
    if (AudioObjectGetPropertyData(aggregate, &address, 0, NULL, &size, format) != noErr) return -1;
    return 0;
}

static void lm_tap_reset_resamplers(lm_macos_capture* capture)
{
    for (unsigned int channel = 0; channel < LM_TAP_MAX_CHANNELS; channel++) {
        capture->resamplers[channel].pending_frames = 0;
        // Первая выходная точка сдвинута на половину окна: так фильтр всегда
        // видит полный набор соседних отсчётов.
        capture->resamplers[channel].phase = LM_TAP_TAPS / 2;
    }
}

// lm_tap_create_aggregate builds the private aggregate device that carries the tap.
static int lm_tap_create_aggregate(lm_macos_capture* capture, char* error_text, size_t error_size)
{
    NSString* uid = lm_tap_default_output_uid();
    if (uid == nil) {
        lm_macos_error(error_text, error_size, @"Не найдено устройство вывода звука");
        return -1;
    }
    NSString* aggregate_uid = [NSString stringWithFormat:@"localmeetassist.system.%@", [NSUUID UUID].UUIDString];
    NSDictionary* description = @{
        @kAudioAggregateDeviceNameKey: @"LocalMeetAssist system audio",
        @kAudioAggregateDeviceUIDKey: aggregate_uid,
        @kAudioAggregateDeviceMainSubDeviceKey: uid,
        @kAudioAggregateDeviceIsPrivateKey: @YES,
        @kAudioAggregateDeviceIsStackedKey: @NO,
        @kAudioAggregateDeviceSubDeviceListKey: @[ @{ @kAudioSubDeviceUIDKey: uid } ],
        @kAudioAggregateDeviceTapListKey: @[ @{ @kAudioSubTapUIDKey: capture->tap_uid, @kAudioSubTapDriftCompensationKey: @YES } ],
    };
    AudioObjectID aggregate = kAudioObjectUnknown;
    OSStatus status = AudioHardwareCreateAggregateDevice((__bridge CFDictionaryRef)description, &aggregate);
    if (status != noErr || aggregate == kAudioObjectUnknown) {
        lm_macos_error(error_text, error_size, [NSString stringWithFormat:@"Не удалось создать агрегатное устройство CoreAudio (OSStatus=%d)", (int)status]);
        return -1;
    }
    capture->aggregate_id = aggregate;
    if (lm_tap_read_aggregate_format(aggregate, &capture->source_format) != 0 || capture->source_format.mSampleRate <= 0) {
        lm_macos_error(error_text, error_size, @"Не удалось определить формат системного звука");
        AudioHardwareDestroyAggregateDevice(aggregate);
        capture->aggregate_id = kAudioObjectUnknown;
        return -1;
    }
    capture->step = capture->source_format.mSampleRate / (double)capture->target_rate;
    lm_tap_reset_resamplers(capture);
    return 0;
}

// lm_tap_start_io starts the IO cycle of the current aggregate device.
static int lm_tap_start_io(lm_macos_capture* capture, char* error_text, size_t error_size)
{
    OSStatus status = AudioDeviceCreateIOProcIDWithBlock(&capture->proc_id, capture->aggregate_id, NULL,
        ^(const AudioTimeStamp* now, const AudioBufferList* input, const AudioTimeStamp* inputTime, AudioBufferList* outputData, const AudioTimeStamp* outputTime) {
            (void)now; (void)inputTime; (void)outputData; (void)outputTime;
            lm_tap_handle_audio(capture, input);
        });
    if (status != noErr || capture->proc_id == NULL) {
        lm_macos_error(error_text, error_size, [NSString stringWithFormat:@"Не удалось создать IOProc системного звука (OSStatus=%d)", (int)status]);
        return -1;
    }
    status = AudioDeviceStart(capture->aggregate_id, capture->proc_id);
    if (status != noErr) {
        lm_macos_error(error_text, error_size, [NSString stringWithFormat:@"Не удалось запустить захват системного звука (OSStatus=%d)", (int)status]);
        AudioDeviceDestroyIOProcID(capture->aggregate_id, capture->proc_id);
        capture->proc_id = NULL;
        return -1;
    }
    return 0;
}

// lm_tap_stop_io stops and releases the IO cycle, keeping the aggregate device.
static void lm_tap_stop_io(lm_macos_capture* capture)
{
    if (capture->proc_id != NULL && capture->aggregate_id != kAudioObjectUnknown) {
        AudioDeviceStop(capture->aggregate_id, capture->proc_id);
        AudioDeviceDestroyIOProcID(capture->aggregate_id, capture->proc_id);
        capture->proc_id = NULL;
    }
}

// lm_tap_rebuild follows a change of the default output device: the aggregate is
// rebuilt on the new device while the tap and the WAV sink stay in place.
static void lm_tap_rebuild(lm_macos_capture* capture)
{
    pthread_mutex_lock(&capture->lock);
    if (!capture->stopping && capture->sink != NULL) {
        lm_tap_stop_io(capture);
        if (capture->aggregate_id != kAudioObjectUnknown) {
            AudioHardwareDestroyAggregateDevice(capture->aggregate_id);
            capture->aggregate_id = kAudioObjectUnknown;
        }
        char error[512] = {0};
        if (lm_tap_create_aggregate(capture, error, sizeof(error)) != 0) {
            lm_macos_capture_fail(capture, error);
        } else if (lm_tap_start_io(capture, error, sizeof(error)) != 0) {
            lm_macos_capture_fail(capture, error);
        }
    }
    pthread_mutex_unlock(&capture->lock);
}

static void lm_tap_free(lm_macos_capture* capture)
{
    for (unsigned int channel = 0; channel < LM_TAP_MAX_CHANNELS; channel++) {
        free(capture->channel_scratch[channel]);
        capture->channel_scratch[channel] = NULL;
    }
    free(capture->output_scratch);
    capture->output_scratch = NULL;
    if (capture->tap_uid != nil) {
        [capture->tap_uid release];
        capture->tap_uid = nil;
    }
    if (capture->queue != NULL) {
        dispatch_release(capture->queue);
        capture->queue = NULL;
    }
    pthread_mutex_destroy(&capture->lock);
    free(capture);
}

void* lm_macos_system_start(const char* path, unsigned int sample_rate, unsigned int channels, char* error_text, size_t error_size)
{
    if (@available(macOS 14.2, *)) {
        @autoreleasepool {
            if (channels < 1) channels = 1;
            if (channels > LM_TAP_MAX_CHANNELS) channels = LM_TAP_MAX_CHANNELS;
            if (sample_rate == 0) {
                lm_macos_error(error_text, error_size, @"Не задана частота дискретизации системного звука");
                return NULL;
            }
            lm_macos_capture* capture = (lm_macos_capture*)calloc(1, sizeof(lm_macos_capture));
            if (capture == NULL) {
                lm_macos_error(error_text, error_size, @"Недостаточно памяти для захвата системного звука");
                return NULL;
            }
            capture->channels = channels;
            capture->target_rate = sample_rate;
            capture->tap_id = kAudioObjectUnknown;
            capture->aggregate_id = kAudioObjectUnknown;
            pthread_mutex_init(&capture->lock, NULL);
            capture->queue = dispatch_queue_create("localmeetassist.system-tap", DISPATCH_QUEUE_SERIAL);
            capture->output_capacity = 4096;
            capture->output_scratch = (int16_t*)malloc(capture->output_capacity * sizeof(int16_t));
            for (unsigned int channel = 0; channel < LM_TAP_MAX_CHANNELS; channel++) {
                capture->channel_scratch[channel] = (float*)malloc(LM_TAP_PENDING * sizeof(float));
            }
            if (capture->output_scratch == NULL || capture->channel_scratch[0] == NULL || capture->channel_scratch[1] == NULL) {
                lm_macos_error(error_text, error_size, @"Недостаточно памяти для буферов системного звука");
                lm_tap_free(capture);
                return NULL;
            }
            capture->sink = lm_platform_wav_open(path, sample_rate, channels);
            if (capture->sink == NULL) {
                lm_macos_error(error_text, error_size, @"Не удалось создать WAV системного звука");
                lm_tap_free(capture);
                return NULL;
            }

            CATapDescription* description = [[CATapDescription alloc] initStereoGlobalTapButExcludeProcesses:@[]];
            description.name = @"LocalMeetAssist system audio";
            description.UUID = [NSUUID UUID];
            description.privateTap = YES;
            description.muteBehavior = CATapUnmuted;
            capture->tap_uid = [description.UUID.UUIDString copy];
            OSStatus status = AudioHardwareCreateProcessTap(description, &capture->tap_id);
            [description release];
            if (status != noErr || capture->tap_id == kAudioObjectUnknown) {
                lm_macos_error(error_text, error_size, [NSString stringWithFormat:@"Не удалось создать тап системного звука (OSStatus=%d)", (int)status]);
                lm_platform_wav_close(capture->sink);
                capture->sink = NULL;
                lm_tap_free(capture);
                return NULL;
            }

            char aggregate_error[512] = {0};
            if (lm_tap_create_aggregate(capture, aggregate_error, sizeof(aggregate_error)) != 0) {
                lm_macos_error(error_text, error_size, [NSString stringWithUTF8String:aggregate_error]);
                AudioHardwareDestroyProcessTap(capture->tap_id);
                lm_platform_wav_close(capture->sink);
                capture->sink = NULL;
                lm_tap_free(capture);
                return NULL;
            }
            if (lm_tap_start_io(capture, aggregate_error, sizeof(aggregate_error)) != 0) {
                lm_macos_error(error_text, error_size, [NSString stringWithUTF8String:aggregate_error]);
                AudioHardwareDestroyAggregateDevice(capture->aggregate_id);
                AudioHardwareDestroyProcessTap(capture->tap_id);
                lm_platform_wav_close(capture->sink);
                capture->sink = NULL;
                lm_tap_free(capture);
                return NULL;
            }

            // Смена устройства вывода не должна обрывать запись: пересобираем
            // агрегатное устройство на новом устройстве, тап и WAV остаются.
            AudioObjectPropertyAddress device_address = {kAudioHardwarePropertyDefaultOutputDevice, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
            capture->device_listener = [^(UInt32 count, const AudioObjectPropertyAddress* addresses) {
                (void)count; (void)addresses;
                lm_tap_rebuild(capture);
            } copy];
            AudioObjectAddPropertyListenerBlock(kAudioObjectSystemObject, &device_address, capture->queue, capture->device_listener);
            return capture;
        }
    }
    lm_macos_error(error_text, error_size, @"Для системного звука требуется macOS 14.2 или новее");
    return NULL;
}

int lm_macos_system_stop(void* opaque, char* error_text, size_t error_size)
{
    if (opaque == NULL) return 0;
    lm_macos_capture* capture = (lm_macos_capture*)opaque;
    pthread_mutex_lock(&capture->lock);
    // Сначала запрещаем пересборку: слушатель мог уже поставить блок в очередь.
    capture->stopping = 1;
    if (capture->device_listener != nil) {
        AudioObjectPropertyAddress device_address = {kAudioHardwarePropertyDefaultOutputDevice, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
        AudioObjectRemovePropertyListenerBlock(kAudioObjectSystemObject, &device_address, capture->queue, capture->device_listener);
        Block_release(capture->device_listener);
        capture->device_listener = nil;
    }
    lm_tap_stop_io(capture);
    if (capture->aggregate_id != kAudioObjectUnknown) {
        AudioHardwareDestroyAggregateDevice(capture->aggregate_id);
        capture->aggregate_id = kAudioObjectUnknown;
    }
    if (capture->tap_id != kAudioObjectUnknown) {
        AudioHardwareDestroyProcessTap(capture->tap_id);
        capture->tap_id = kAudioObjectUnknown;
    }
    pthread_mutex_unlock(&capture->lock);
    // Дренируем очередь: блок смены устройства мог быть уже запущен, и он не
    // должен работать с освобождённой памятью.
    dispatch_sync(capture->queue, ^{});
    int callback_failed = capture->failed;
    char callback_reason[sizeof(capture->failure_reason)];
    snprintf(callback_reason, sizeof(callback_reason), "%s", capture->failure_reason);
    int wav_error = lm_platform_wav_close(capture->sink);
    capture->sink = NULL;
    lm_tap_free(capture);
    int failed = callback_failed || wav_error != 0;
    if (failed && error_text != NULL && error_text[0] == '\0') {
        if (callback_failed && callback_reason[0] != '\0') {
            snprintf(error_text, error_size, "%s", callback_reason);
        } else {
            lm_macos_error(error_text, error_size, @"Не удалось закрыть WAV системного аудио macOS");
        }
    }
    return failed ? -1 : 0;
}
