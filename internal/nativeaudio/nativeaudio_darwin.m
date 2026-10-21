#import <AppKit/NSRunningApplication.h>
#import <Foundation/Foundation.h>
#import <AudioToolbox/AudioToolbox.h>
#import <CoreAudio/CoreAudio.h>
#import <CoreAudio/AudioHardwareTapping.h>
#import <CoreAudio/CATapDescription.h>

#include <math.h>
#include <mach/mach_time.h>
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

// lm_macos_is_own_aggregate reports whether the device is the private aggregate
// this process created for the system tap. Its output is running while a meeting
// is recorded, and counting it would keep the activity flag set forever.
static int lm_macos_is_own_aggregate(AudioObjectID device)
{
    AudioObjectPropertyAddress address = {kAudioDevicePropertyDeviceUID, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
    CFStringRef uid = NULL;
    UInt32 size = sizeof(uid);
    if (AudioObjectGetPropertyData(device, &address, 0, NULL, &size, &uid) != noErr || uid == NULL) return 0;
    int own = CFStringHasPrefix(uid, CFSTR("localmeetassist.system."));
    CFRelease(uid);
    return own;
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
        if (lm_macos_is_own_aggregate(devices[index])) continue;
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

// lm_macos_any_foreign_process_running reports whether another process uses the
// given direction. known stays 0 on macOS versions without the per-process API,
// so the caller can fall back to the device scan.
static int lm_macos_any_foreign_process_running(AudioObjectPropertySelector selector, int* active, int* known)
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
            selector,
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
    if (lm_macos_any_foreign_process_running(kAudioProcessPropertyIsRunningInput, &mic_active, &mic_known) != 0) mic_known = 0;
    if (!mic_known && lm_macos_any_device_running(kAudioDevicePropertyScopeInput, &mic_active) != 0) {
        if (error_text != NULL && error_size > 0) snprintf(error_text, error_size, "%s", "CoreAudio activity query failed");
        return -1;
    }
    // Активность вывода считается по процессам: устройство остаётся «занятым»
    // и из-за нашего собственного агрегатного устройства с тапом, из-за чего
    // автостоп встречи не срабатывал.
    int sys_active = 0;
    int sys_known = 0;
    if (lm_macos_any_foreign_process_running(kAudioProcessPropertyIsRunningOutput, &sys_active, &sys_known) != 0) sys_known = 0;
    if (!sys_known && lm_macos_any_device_running(kAudioDevicePropertyScopeOutput, &sys_active) != 0) {
        if (error_text != NULL && error_size > 0) snprintf(error_text, error_size, "%s", "CoreAudio activity query failed");
        return -1;
    }
    *microphone_active = mic_active;
    *system_active = sys_active;
    return 0;
}

// lm_macos_process_label resolves the localized application name, the bundle
// identifier or the process name of a pid, in that order.
static void lm_macos_process_label(pid_t pid, char* label, size_t label_size)
{
    if (label == NULL || label_size == 0) return;
    label[0] = '\0';
    NSRunningApplication* application = [NSRunningApplication runningApplicationWithProcessIdentifier:pid];
    if (application != nil) {
        if (application.localizedName != nil && application.localizedName.length > 0) {
            snprintf(label, label_size, "%s", [application.localizedName UTF8String]);
        }
        if (label[0] == '\0' && application.bundleIdentifier != nil && application.bundleIdentifier.length > 0) {
            snprintf(label, label_size, "%s", [application.bundleIdentifier UTF8String]);
        }
    }
    if (label[0] == '\0') {
        char process_name[512];
        int name_len = proc_name(pid, process_name, sizeof(process_name));
        if (name_len > 0) {
            process_name[sizeof(process_name) - 1] = '\0';
            snprintf(label, label_size, "%s", process_name);
        }
    }
}

// lm_macos_active_audio_apps writes newline-separated names of the applications
// that currently use the microphone or play audio, excluding this process. It
// lets the detector stop a recording when the application that started it is
// done, even if another application keeps playing something.
int lm_macos_active_audio_apps(char* names, size_t names_size, int* found, char* error_text, size_t error_size)
{
    (void)error_text;
    (void)error_size;
    if (names == NULL || names_size == 0 || found == NULL) return -1;
    names[0] = '\0';
    *found = 0;

    AudioObjectPropertyAddress address = {kAudioHardwarePropertyProcessObjectList, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
    UInt32 size = 0;
    if (AudioObjectGetPropertyDataSize(kAudioObjectSystemObject, &address, 0, NULL, &size) != noErr || size == 0) return -1;
    int count = (int)(size / sizeof(AudioObjectID));
    AudioObjectID* objects = (AudioObjectID*)calloc((size_t)count, sizeof(AudioObjectID));
    if (objects == NULL) return -1;
    if (AudioObjectGetPropertyData(kAudioObjectSystemObject, &address, 0, NULL, &size, objects) != noErr) {
        free(objects);
        return -1;
    }
    *found = 1;
    size_t used = 0;
    for (int index = 0; index < count; index++) {
        UInt32 running_in = 0, running_out = 0;
        UInt32 running_size = sizeof(running_in);
        AudioObjectPropertyAddress in_address = {kAudioProcessPropertyIsRunningInput, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
        AudioObjectPropertyAddress out_address = {kAudioProcessPropertyIsRunningOutput, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
        if (AudioObjectGetPropertyData(objects[index], &in_address, 0, NULL, &running_size, &running_in) != noErr) running_in = 0;
        running_size = sizeof(running_out);
        if (AudioObjectGetPropertyData(objects[index], &out_address, 0, NULL, &running_size, &running_out) != noErr) running_out = 0;
        if (running_in == 0 && running_out == 0) continue;
        pid_t pid = 0;
        UInt32 pid_size = sizeof(pid);
        AudioObjectPropertyAddress pid_address = {kAudioProcessPropertyPID, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
        if (AudioObjectGetPropertyData(objects[index], &pid_address, 0, NULL, &pid_size, &pid) != noErr || pid <= 0) continue;
        if (pid == getpid()) continue;
        char label[512];
        lm_macos_process_label(pid, label, sizeof(label));
        if (label[0] == '\0') continue;
        size_t length = strlen(label);
        if (used + length + 2 > names_size) break;
        if (used > 0) names[used++] = '\n';
        memcpy(names + used, label, length);
        used += length;
        names[used] = '\0';
    }
    free(objects);
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

        char label[512];
        lm_macos_process_label(pid, label, sizeof(label));
        if (label[0] == '\0') continue;
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
    // UID of the output device the aggregate is currently built on: a repeated
    // notification for the same device must not rebuild anything.
    NSString* device_uid;
    // Set before the teardown so a queued device-change block does not rebuild.
    int stopping;
    AudioObjectPropertyListenerBlock device_listener;
    dispatch_queue_t queue;
    pthread_mutex_t lock;
    void* sink;
    unsigned int channels;
    unsigned int target_rate;
    // Формат самого тапа, а не агрегатного устройства: если у устройства вывода
    // есть микрофон (гарнитура, USB-аудио), первым входным потоком агрегата идёт
    // он, и формат устройства описывает микрофон.
    AudioStreamBasicDescription source_format;
    unsigned int stream_first_buffer;
    int stream_interleaved;
    // Частота, в которой CoreAudio реально отдаёт данные: у Bluetooth-гарнитуры
    // тап заявляет 48 кГц, а поток идёт в частоте устройства (44.1 кГц).
    double input_rate;
    double step;
    // Выравнивание по времени хоста: между колбэками (и при пересоздании
    // агрегата) CoreAudio может отдать больше или меньше данных, чем прошло
    // времени. Лишнее пропускаем, недостающее дополняем тишиной, чтобы дорожка
    // не уезжала относительно встречи.
    uint64_t last_host_time;
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

// lm_read_stream_sample reads one sample of the stream that starts at the given
// buffer, so the microphone buffers of an aggregate device are ignored.
static float lm_read_stream_sample(const AudioBufferList* list, unsigned int first_buffer, const AudioStreamBasicDescription* format, int interleaved, size_t frame, unsigned int channel)
{
    const AudioBuffer* buffer = interleaved ? &list->mBuffers[first_buffer] : &list->mBuffers[first_buffer + channel];
    size_t index = interleaved ? frame * format->mChannelsPerFrame + channel : frame;
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
// lm_tap_ticks_per_second converts mach host time ticks into seconds.
static double lm_tap_ticks_per_second(void)
{
    static double cached = 0;
    if (cached > 0) return cached;
    mach_timebase_info_data_t info;
    if (mach_timebase_info(&info) != KERN_SUCCESS || info.denom == 0) {
        cached = 1e9;
        return cached;
    }
    cached = 1e9 * (double)info.denom / (double)info.numer;
    return cached;
}

// lm_tap_write_silence pads the output so a gap in delivery keeps the track
// aligned with the meeting timeline.
static void lm_tap_write_silence(lm_macos_capture* capture, size_t input_frames)
{
    if (capture->step <= 0) return;
    size_t frames = (size_t)((double)input_frames / capture->step);
    if (frames == 0) return;
    memset(capture->output_scratch, 0, capture->output_capacity * sizeof(int16_t));
    while (frames > 0) {
        size_t chunk = frames;
        size_t room = capture->output_capacity / capture->channels;
        if (chunk > room) chunk = room;
        if (lm_platform_wav_write(capture->sink, capture->output_scratch, chunk * capture->channels * sizeof(int16_t)) != 0) {
            lm_macos_capture_fail(capture, "Ошибка записи системного аудио в WAV");
            return;
        }
        capture->frames_written += (uint64_t)chunk;
        frames -= chunk;
    }
}

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
static void lm_tap_handle_audio(lm_macos_capture* capture, const AudioBufferList* input, uint64_t host_time)
{
    if (capture->failed || capture->sink == NULL || input == NULL) return;
    // Пересоздание агрегата меняет формат, шаг и буферы; без блокировки
    // обратный вызов мог бы прочитать наполовину обновлённое состояние.
    pthread_mutex_lock(&capture->lock);
    const AudioStreamBasicDescription* format = &capture->source_format;
    if (format->mChannelsPerFrame == 0) {
        pthread_mutex_unlock(&capture->lock);
        return;
    }
    int interleaved = capture->stream_interleaved;
    unsigned int first_buffer = capture->stream_first_buffer;
    unsigned int needed_buffers = interleaved ? first_buffer + 1 : first_buffer + format->mChannelsPerFrame;
    if (input->mNumberBuffers < needed_buffers) {
        lm_macos_capture_fail(capture, "CoreAudio вернул неполный список аудиобуферов");
        pthread_mutex_unlock(&capture->lock);
        return;
    }
    size_t frame_bytes = interleaved ? format->mBytesPerFrame : (format->mBitsPerChannel / 8);
    size_t frames = frame_bytes > 0 ? input->mBuffers[first_buffer].mDataByteSize / frame_bytes : 0;
    if (frames == 0) {
        pthread_mutex_unlock(&capture->lock);
        return;
    }
    size_t skip = 0;
    if (host_time != 0 && capture->last_host_time != 0 && capture->input_rate > 0) {
        double elapsed = (double)(host_time - capture->last_host_time) / lm_tap_ticks_per_second();
        double expected = elapsed * capture->input_rate;
        double delta = (double)frames - expected;
        double tolerance = 0.08 * capture->input_rate;
        if (delta > tolerance) {
            skip = (size_t)(delta > (double)frames ? (double)frames : delta);
        } else if (delta < -tolerance) {
            lm_tap_write_silence(capture, (size_t)(-delta));
        }
    }
    capture->last_host_time = host_time;
    if (skip >= frames) {
        pthread_mutex_unlock(&capture->lock);
        return;
    }
    frames -= skip;
    for (unsigned int channel = 0; channel < capture->channels; channel++) {
        lm_tap_resampler* resampler = &capture->resamplers[channel];
        float* scratch = capture->channel_scratch[channel];
        unsigned int source_channel = channel < format->mChannelsPerFrame ? channel : format->mChannelsPerFrame - 1;
        for (size_t frame = 0; frame < frames; frame++) {
            size_t source_frame = frame + skip;
            float value;
            if (capture->channels == 1 && format->mChannelsPerFrame > 1) {
                float mixed = 0.0f;
                for (unsigned int other = 0; other < format->mChannelsPerFrame; other++) {
                    mixed += lm_read_stream_sample(input, first_buffer, format, interleaved, source_frame, other);
                }
                value = mixed / (float)format->mChannelsPerFrame;
            } else {
                value = lm_read_stream_sample(input, first_buffer, format, interleaved, source_frame, source_channel);
            }
            scratch[frame] = value;
        }
        int space = LM_TAP_PENDING - resampler->pending_frames;
        if ((int)frames > space) frames = (size_t)space;
        memcpy(resampler->pending + resampler->pending_frames, scratch, sizeof(float) * frames);
        resampler->pending_frames += (int)frames;
    }
    lm_tap_flush(capture);
    pthread_mutex_unlock(&capture->lock);
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

// lm_tap_read_tap_format reads the format the tap itself produces.
static int lm_tap_read_tap_format(AudioObjectID tap, AudioStreamBasicDescription* format)
{
    AudioObjectPropertyAddress address = {kAudioTapPropertyFormat, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
    UInt32 size = sizeof(*format);
    if (AudioObjectGetPropertyData(tap, &address, 0, NULL, &size, format) != noErr) return -1;
    return format->mChannelsPerFrame > 0 ? 0 : -1;
}

// lm_tap_find_tap_stream finds the buffer of the tap inside the aggregate's input
// buffer list. A stream occupies one buffer when interleaved and one buffer per
// channel otherwise, so the tap's offset is the sum of the streams before it.
// The tap is added after the sub-devices, so the last matching stream wins.
static int lm_tap_find_tap_stream(lm_macos_capture* capture, char* error_text, size_t error_size)
{
    AudioObjectPropertyAddress list_address = {kAudioDevicePropertyStreams, kAudioDevicePropertyScopeInput, kAudioObjectPropertyElementMain};
    UInt32 size = 0;
    if (AudioObjectGetPropertyDataSize(capture->aggregate_id, &list_address, 0, NULL, &size) != noErr || size < sizeof(AudioStreamID)) {
        lm_macos_error(error_text, error_size, @"Не удалось получить входные потоки агрегатного устройства");
        return -1;
    }
    unsigned int count = size / sizeof(AudioStreamID);
    AudioStreamID* streams = (AudioStreamID*)malloc(size);
    if (streams == NULL) {
        lm_macos_error(error_text, error_size, @"Недостаточно памяти для потоков агрегатного устройства");
        return -1;
    }
    if (AudioObjectGetPropertyData(capture->aggregate_id, &list_address, 0, NULL, &size, streams) != noErr) {
        free(streams);
        lm_macos_error(error_text, error_size, @"Не удалось получить входные потоки агрегатного устройства");
        return -1;
    }
    const AudioStreamBasicDescription tap = capture->source_format;
    unsigned int offset = 0;
    int chosen_offset = -1;
    int chosen_interleaved = 1;
    AudioStreamBasicDescription chosen_format = tap;
    int last_offset = 0;
    int last_interleaved = 1;
    AudioStreamBasicDescription last_format = tap;
    unsigned int last_channels = 0;
    for (unsigned int index = 0; index < count; index++) {
        AudioObjectPropertyAddress format_address = {kAudioStreamPropertyVirtualFormat, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
        AudioStreamBasicDescription stream = {0};
        UInt32 format_size = sizeof(stream);
        if (AudioObjectGetPropertyData(streams[index], &format_address, 0, NULL, &format_size, &stream) != noErr) {
            stream = tap;
        }
        int interleaved = (stream.mFormatFlags & kAudioFormatFlagIsNonInterleaved) == 0;
        if (stream.mSampleRate == tap.mSampleRate &&
            stream.mChannelsPerFrame == tap.mChannelsPerFrame &&
            stream.mBytesPerFrame == tap.mBytesPerFrame) {
            chosen_offset = (int)offset;
            chosen_interleaved = interleaved;
            chosen_format = stream;
        }
        last_offset = (int)offset;
        last_interleaved = interleaved;
        last_format = stream;
        last_channels = stream.mChannelsPerFrame;
        offset += interleaved ? 1 : (stream.mChannelsPerFrame > 0 ? stream.mChannelsPerFrame : 1);
    }
    free(streams);
    if (chosen_offset < 0) {
        // Формат мог измениться из-за дрейф-компенсации: берём последний поток,
        // он принадлежит тапу, и сообщаем об этом в лог.
        chosen_offset = last_offset;
        chosen_interleaved = last_interleaved;
        chosen_format = last_format;
        fprintf(stderr, "system audio tap: stream format %.0f Hz %u ch differs from the tap (%.0f Hz %u ch), using the last stream of %u ch\n",
            chosen_format.mSampleRate, chosen_format.mChannelsPerFrame, tap.mSampleRate, tap.mChannelsPerFrame, last_channels);
    }
    // Дальше декодируем и пересчитываем по формату выбранного потока: при
    // дрейф-компенсации CoreAudio может отдавать тап в частоте суб-устройства.
    capture->source_format = chosen_format;
    capture->stream_first_buffer = (unsigned int)chosen_offset;
    capture->stream_interleaved = chosen_interleaved;
    fprintf(stderr, "system audio tap: %.0f Hz %u ch, buffer %u of %u, interleaved=%d\n",
        capture->source_format.mSampleRate, capture->source_format.mChannelsPerFrame,
        capture->stream_first_buffer, count, chosen_interleaved);
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
    if (capture->device_uid != nil) {
        [capture->device_uid release];
    }
    capture->device_uid = [uid copy];
    if (lm_tap_read_tap_format(capture->tap_id, &capture->source_format) != 0 || capture->source_format.mSampleRate <= 0) {
        lm_macos_error(error_text, error_size, @"Не удалось определить формат системного звука");
        AudioHardwareDestroyAggregateDevice(aggregate);
        capture->aggregate_id = kAudioObjectUnknown;
        return -1;
    }
    if (lm_tap_find_tap_stream(capture, error_text, error_size) != 0) {
        AudioHardwareDestroyAggregateDevice(aggregate);
        capture->aggregate_id = kAudioObjectUnknown;
        return -1;
    }
    if (capture->source_format.mSampleRate <= 0) {
        lm_macos_error(error_text, error_size, @"Не удалось определить частоту системного звука");
        AudioHardwareDestroyAggregateDevice(aggregate);
        capture->aggregate_id = kAudioObjectUnknown;
        return -1;
    }
    // Шаг пересчёта считается по частоте агрегатного устройства: IOProc отдаёт
    // кадры в его такте, даже если тап заявляет другую частоту.
    capture->input_rate = capture->source_format.mSampleRate;
    Float64 clock_rate = 0;
    UInt32 clock_size = sizeof(clock_rate);
    AudioObjectPropertyAddress rate_address = {kAudioDevicePropertyNominalSampleRate, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
    if (AudioObjectGetPropertyData(aggregate, &rate_address, 0, NULL, &clock_size, &clock_rate) == noErr && clock_rate > 0) {
        capture->input_rate = clock_rate;
    }
    capture->step = capture->input_rate / (double)capture->target_rate;
    lm_tap_reset_resamplers(capture);
    fprintf(stderr, "system audio tap: capturing %.0f Hz into %.0f Hz (step %.4f)\n",
        capture->input_rate, (double)capture->target_rate, capture->step);
    return 0;
}

// lm_tap_start_io starts the IO cycle of the current aggregate device.
static int lm_tap_start_io(lm_macos_capture* capture, char* error_text, size_t error_size)
{
    OSStatus status = AudioDeviceCreateIOProcIDWithBlock(&capture->proc_id, capture->aggregate_id, NULL,
        ^(const AudioTimeStamp* now, const AudioBufferList* input, const AudioTimeStamp* inputTime, AudioBufferList* outputData, const AudioTimeStamp* outputTime) {
            (void)inputTime; (void)outputData; (void)outputTime;
            uint64_t host_time = now != NULL ? now->mHostTime : 0;
            if (host_time == 0 && inputTime != NULL) host_time = inputTime->mHostTime;
            lm_tap_handle_audio(capture, input, host_time);
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
    NSString* current = lm_tap_default_output_uid();
    int unchanged = current != nil && capture->device_uid != nil && [current isEqualToString:capture->device_uid];
    if (!unchanged && !capture->stopping && capture->sink != NULL) {
        lm_tap_stop_io(capture);
        if (capture->aggregate_id != kAudioObjectUnknown) {
            AudioHardwareDestroyAggregateDevice(capture->aggregate_id);
            capture->aggregate_id = kAudioObjectUnknown;
        }
        char error[512] = {0};
        uint64_t rebuild_started = mach_absolute_time();
        if (lm_tap_create_aggregate(capture, error, sizeof(error)) != 0) {
            lm_macos_capture_fail(capture, error);
        } else if (lm_tap_start_io(capture, error, sizeof(error)) != 0) {
            lm_macos_capture_fail(capture, error);
        } else {
            // Пока агрегат пересоздавался, звук не захватывался. Дописываем эту
            // паузу тишиной, иначе дорожка сдвинется относительно встречи.
            double downtime = (double)(mach_absolute_time() - rebuild_started) / lm_tap_ticks_per_second();
            if (downtime > 0.02 && capture->input_rate > 0) {
                lm_tap_write_silence(capture, (size_t)(downtime * capture->input_rate));
            }
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
    if (capture->device_uid != nil) {
        [capture->device_uid release];
        capture->device_uid = nil;
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
