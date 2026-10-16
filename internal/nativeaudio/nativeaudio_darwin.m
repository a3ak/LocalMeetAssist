#import <Foundation/Foundation.h>
#import <ScreenCaptureKit/ScreenCaptureKit.h>
#import <CoreMedia/CoreMedia.h>
#import <AudioToolbox/AudioToolbox.h>
#import <CoreAudio/CoreAudio.h>

#include <math.h>
#include <stdint.h>
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

typedef struct {
    SCStream* stream;
    id output;
    dispatch_queue_t queue;
    void* sink;
    unsigned int channels;
    int failed;
    char failure_reason[512];
    uint64_t frames_written;
} lm_macos_capture;

@interface LMAudioStreamOutput : NSObject <SCStreamOutput> {
@public
    lm_macos_capture* _capture;
}
- (instancetype)initWithCapture:(lm_macos_capture*)capture;
@end

static void lm_macos_capture_fail(lm_macos_capture* capture, const char* message)
{
    if (capture == NULL) return;
    capture->failed = 1;
    if (capture->failure_reason[0] == '\0' && message != NULL) {
        snprintf(capture->failure_reason, sizeof(capture->failure_reason), "%s", message);
    }
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

@implementation LMAudioStreamOutput
- (instancetype)initWithCapture:(lm_macos_capture*)capture
{
    self = [super init];
    if (self != nil) {
        _capture = capture;
    }
    return self;
}

- (void)stream:(SCStream*)stream didOutputSampleBuffer:(CMSampleBufferRef)sampleBuffer ofType:(SCStreamOutputType)type
{
    (void)stream;
    if (type != SCStreamOutputTypeAudio || sampleBuffer == NULL || !CMSampleBufferDataIsReady(sampleBuffer)) return;
    lm_macos_capture* capture = _capture;
    if (capture == NULL || capture->sink == NULL) return;
    CMFormatDescriptionRef description = CMSampleBufferGetFormatDescription(sampleBuffer);
    const AudioStreamBasicDescription* format = CMAudioFormatDescriptionGetStreamBasicDescription(description);
    if (format == NULL || format->mFormatID != kAudioFormatLinearPCM || format->mChannelsPerFrame == 0) {
        lm_macos_capture_fail(capture, "ScreenCaptureKit вернул неподдерживаемый формат аудио");
        return;
    }
    int supported = (((format->mFormatFlags & kAudioFormatFlagIsFloat) != 0 && format->mBitsPerChannel == 32) ||
                     ((format->mFormatFlags & kAudioFormatFlagIsSignedInteger) != 0 &&
                      (format->mBitsPerChannel == 16 || format->mBitsPerChannel == 32)));
    if (!supported) {
        lm_macos_capture_fail(capture, "ScreenCaptureKit вернул неподдерживаемый PCM-формат");
        return;
    }
    /* CoreMedia may require more storage than sizeof(AudioBufferList), including
       alignment padding. Ask for the exact size first instead of guessing a
       maximum number of AudioBuffer entries. The probe normally returns
       kCMSampleBufferError_ArrayTooSmall together with the required size. */
    size_t list_size = 0;
    CMBlockBufferRef probe_block = NULL;
    OSStatus probe_status = CMSampleBufferGetAudioBufferListWithRetainedBlockBuffer(
        sampleBuffer, &list_size, NULL, 0, kCFAllocatorDefault, kCFAllocatorDefault,
        kCMSampleBufferFlag_AudioBufferList_Assure16ByteAlignment, &probe_block);
    if (probe_block != NULL) CFRelease(probe_block);
    if (list_size < sizeof(AudioBufferList) ||
        (probe_status != noErr && probe_status != kCMSampleBufferError_ArrayTooSmall)) {
        char message[224];
        snprintf(message, sizeof(message),
                 "Не удалось определить размер буфера ScreenCaptureKit (OSStatus=%d, size=%zu)",
                 (int)probe_status, list_size);
        lm_macos_capture_fail(capture, message);
        return;
    }
    AudioBufferList* list = (AudioBufferList*)calloc(1, list_size);
    if (list == NULL) {
        lm_macos_capture_fail(capture, "Недостаточно памяти для буфера системного аудио");
        return;
    }
    CMBlockBufferRef block = NULL;
    size_t actual_list_size = list_size;
    OSStatus status = CMSampleBufferGetAudioBufferListWithRetainedBlockBuffer(
        sampleBuffer, &actual_list_size, list, list_size, kCFAllocatorDefault, kCFAllocatorDefault,
        kCMSampleBufferFlag_AudioBufferList_Assure16ByteAlignment, &block);
    if (status != noErr) {
        free(list);
        char message[224];
        snprintf(message, sizeof(message),
                 "Не удалось получить буфер ScreenCaptureKit (OSStatus=%d, allocated=%zu, required=%zu)",
                 (int)status, list_size, actual_list_size);
        lm_macos_capture_fail(capture, message);
        return;
    }
    CMItemCount frames = CMSampleBufferGetNumSamples(sampleBuffer);
    if (frames <= 0) {
        if (block != NULL) CFRelease(block);
        free(list);
        return;
    }
    int non_interleaved = (format->mFormatFlags & kAudioFormatFlagIsNonInterleaved) != 0;
    unsigned int needed_buffers = non_interleaved ? format->mChannelsPerFrame : 1;
    if (list->mNumberBuffers < needed_buffers) {
        if (block != NULL) CFRelease(block);
        free(list);
        lm_macos_capture_fail(capture, "ScreenCaptureKit вернул неполный список аудиобуферов");
        return;
    }
    for (unsigned int index = 0; index < needed_buffers; index++) {
        size_t needed_bytes = (size_t)frames * format->mBytesPerFrame;
        if (list->mBuffers[index].mData == NULL || list->mBuffers[index].mDataByteSize < needed_bytes) {
            if (block != NULL) CFRelease(block);
            free(list);
            lm_macos_capture_fail(capture, "ScreenCaptureKit вернул повреждённый аудиобуфер");
            return;
        }
    }
    unsigned int target_channels = capture->channels;
    int16_t* output = (int16_t*)malloc((size_t)frames * target_channels * sizeof(int16_t));
    if (output == NULL) {
        if (block != NULL) CFRelease(block);
        free(list);
        lm_macos_capture_fail(capture, "Недостаточно памяти для преобразования системного аудио");
        return;
    }
    for (CMItemCount frame = 0; frame < frames; frame++) {
        if (target_channels == 1) {
            float mixed = 0.0f;
            for (unsigned int channel = 0; channel < format->mChannelsPerFrame; channel++) {
                mixed += lm_read_sample(list, format, (size_t)frame, channel);
            }
            output[frame] = lm_float_to_s16(mixed / format->mChannelsPerFrame);
        } else {
            for (unsigned int channel = 0; channel < target_channels; channel++) {
                unsigned int source_channel = channel < format->mChannelsPerFrame ? channel : format->mChannelsPerFrame - 1;
                output[(size_t)frame * target_channels + channel] = lm_float_to_s16(lm_read_sample(list, format, (size_t)frame, source_channel));
            }
        }
    }
    if (lm_platform_wav_write(capture->sink, output, (size_t)frames * target_channels * sizeof(int16_t)) != 0) {
        lm_macos_capture_fail(capture, "Ошибка записи системного аудио в WAV");
    } else {
        capture->frames_written += (uint64_t)frames;
    }
    free(output);
    if (block != NULL) CFRelease(block);
    free(list);
}
@end

static void lm_macos_error(char* target, size_t size, NSString* message)
{
    if (target == NULL || size == 0) return;
    snprintf(target, size, "%s", message != nil ? [message UTF8String] : "unknown ScreenCaptureKit error");
}

void* lm_macos_system_start(const char* path, unsigned int sample_rate, unsigned int channels, char* error_text, size_t error_size)
{
    if (@available(macOS 13.0, *)) {
        @autoreleasepool {
            lm_macos_capture* capture = (lm_macos_capture*)calloc(1, sizeof(lm_macos_capture));
            if (capture == NULL) {
                lm_macos_error(error_text, error_size, @"Недостаточно памяти для ScreenCaptureKit");
                return NULL;
            }
            capture->sink = lm_platform_wav_open(path, sample_rate, channels);
            capture->channels = channels;
            if (capture->sink == NULL) {
                lm_macos_error(error_text, error_size, @"Не удалось создать WAV системного звука");
                free(capture);
                return NULL;
            }

            dispatch_semaphore_t content_semaphore = dispatch_semaphore_create(0);
            __block SCShareableContent* content = nil;
            __block NSError* content_error = nil;
            [SCShareableContent getShareableContentExcludingDesktopWindows:NO onScreenWindowsOnly:YES completionHandler:^(SCShareableContent* value, NSError* error) {
                content = [value retain];
                content_error = [error retain];
                dispatch_semaphore_signal(content_semaphore);
            }];
            if (dispatch_semaphore_wait(content_semaphore, dispatch_time(DISPATCH_TIME_NOW, 15LL * NSEC_PER_SEC)) != 0) {
                lm_macos_error(error_text, error_size, @"Истекло время ожидания разрешения ScreenCaptureKit");
                lm_platform_wav_close(capture->sink);
                free(capture);
                return NULL;
            }
            if (content_error != nil || content.displays.count == 0) {
                NSString* message = content_error != nil ? content_error.localizedDescription : @"ScreenCaptureKit не вернул доступный экран";
                lm_macos_error(error_text, error_size, message);
                [content_error release];
                [content release];
                lm_platform_wav_close(capture->sink);
                free(capture);
                return NULL;
            }

            SCDisplay* display = [content.displays objectAtIndex:0];
            SCContentFilter* filter = [[SCContentFilter alloc] initWithDisplay:display excludingWindows:@[]];
            SCStreamConfiguration* configuration = [[SCStreamConfiguration alloc] init];
            configuration.width = 2;
            configuration.height = 2;
            configuration.minimumFrameInterval = CMTimeMake(1, 1);
            configuration.queueDepth = 3;
            configuration.showsCursor = NO;
            configuration.capturesAudio = YES;
            configuration.sampleRate = (NSInteger)sample_rate;
            configuration.channelCount = (NSInteger)channels;
            if (@available(macOS 13.0, *)) configuration.excludesCurrentProcessAudio = NO;

            capture->queue = dispatch_queue_create("localmeetassist.system-audio", DISPATCH_QUEUE_SERIAL);
            LMAudioStreamOutput* output = [[LMAudioStreamOutput alloc] initWithCapture:capture];
            SCStream* stream = [[SCStream alloc] initWithFilter:filter configuration:configuration delegate:nil];
            NSError* add_error = nil;
            BOOL added = [stream addStreamOutput:output type:SCStreamOutputTypeAudio sampleHandlerQueue:capture->queue error:&add_error];
            [configuration release];
            [filter release];
            [content_error release];
            [content release];
            if (!added) {
                lm_macos_error(error_text, error_size, add_error.localizedDescription);
                [stream release];
                [output release];
                lm_platform_wav_close(capture->sink);
                free(capture);
                return NULL;
            }

            dispatch_semaphore_t start_semaphore = dispatch_semaphore_create(0);
            __block NSError* start_error = nil;
            [stream startCaptureWithCompletionHandler:^(NSError* error) {
                start_error = [error retain];
                dispatch_semaphore_signal(start_semaphore);
            }];
            if (dispatch_semaphore_wait(start_semaphore, dispatch_time(DISPATCH_TIME_NOW, 15LL * NSEC_PER_SEC)) != 0 || start_error != nil) {
                lm_macos_error(error_text, error_size, start_error != nil ? start_error.localizedDescription : @"Истекло время запуска ScreenCaptureKit");
                [start_error release];
                [stream release];
                [output release];
                lm_platform_wav_close(capture->sink);
                free(capture);
                return NULL;
            }
            [start_error release];
            capture->stream = stream;
            capture->output = output;
            return capture;
        }
    }
    lm_macos_error(error_text, error_size, @"Для системного звука требуется macOS 13 или новее");
    return NULL;
}

int lm_macos_system_stop(void* opaque, char* error_text, size_t error_size)
{
    if (opaque == NULL) return 0;
    if (@available(macOS 13.0, *)) {
        @autoreleasepool {
            lm_macos_capture* capture = (lm_macos_capture*)opaque;
            dispatch_semaphore_t semaphore = dispatch_semaphore_create(0);
            __block NSError* stop_error = nil;
            [capture->stream stopCaptureWithCompletionHandler:^(NSError* error) {
                stop_error = [error retain];
                dispatch_semaphore_signal(semaphore);
            }];
            int timeout = dispatch_semaphore_wait(semaphore, dispatch_time(DISPATCH_TIME_NOW, 15LL * NSEC_PER_SEC)) != 0;
            if (timeout) lm_macos_error(error_text, error_size, @"Истекло время остановки ScreenCaptureKit");
            if (stop_error != nil) lm_macos_error(error_text, error_size, stop_error.localizedDescription);
            NSError* remove_error = nil;
            [capture->stream removeStreamOutput:capture->output type:SCStreamOutputTypeAudio error:&remove_error];
            /* stopCapture completion does not guarantee that every callback already
               queued on sampleHandlerQueue has returned. Drain the serial queue
               before closing/freeing the WAV sink. */
            dispatch_sync(capture->queue, ^{});
            int callback_failed = capture->failed;
            char callback_reason[sizeof(capture->failure_reason)];
            snprintf(callback_reason, sizeof(callback_reason), "%s", capture->failure_reason);
            [stop_error release];
            [capture->stream release];
            [capture->output release];
            int wav_error = lm_platform_wav_close(capture->sink);
            /* Ошибка самого stopCapture не делает остановку неудачной: система
               могла уже закрыть поток сама (например, на длинной записи или при
               смене разрешений на захват), и localizedDescription тогда говорит,
               что трансляция уже остановлена. Объекты освобождаются ниже, а
               очередь дренируется выше, поэтому захват прекращается в любом
               случае. Фатальны только таймаут, сбой колбэков и ошибка WAV:
               именно они означают потерю записанного звука. */
            int failed = timeout || callback_failed || wav_error != 0;
            free(capture);
            if (failed && error_text != NULL && error_text[0] == '\0') {
                if (callback_failed && callback_reason[0] != '\0') {
                    snprintf(error_text, error_size, "%s", callback_reason);
                } else if (wav_error != 0) {
                    lm_macos_error(error_text, error_size, @"Не удалось закрыть WAV системного аудио macOS");
                } else {
                    lm_macos_error(error_text, error_size, @"Ошибка остановки системного аудио macOS");
                }
            }
            return failed ? -1 : 0;
        }
    }
    lm_macos_error(error_text, error_size, @"Для системного звука требуется macOS 13 или новее");
    return -1;
}
