#define MA_NO_DECODING
#define MA_NO_ENCODING
#define MA_NO_RESOURCE_MANAGER
#define MA_NO_NODE_GRAPH
#define MA_NO_ENGINE
#define MINIAUDIO_IMPLEMENTATION
#include "miniaudio.h"
#include "nativeaudio.h"

#include <ctype.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#if defined(_WIN32)
#include <windows.h>
#endif

#if defined(__APPLE__)
void* lm_macos_system_start(const char* path, unsigned int sample_rate, unsigned int channels, char* error_text, size_t error_size);
int lm_macos_system_stop(void* handle, char* error_text, size_t error_size);
#endif

typedef struct {
    FILE* file;
    uint32_t sample_rate;
    uint16_t channels;
    uint64_t data_bytes;
    int failed;
} lm_wav_sink;

struct lm_capture {
    ma_context context;
    int context_initialized;
    ma_device microphone;
    int microphone_initialized;
    int microphone_started;
#if !defined(__APPLE__)
    ma_device system;
    int system_initialized;
    int system_started;
#else
    void* macos_system;
#endif
    lm_wav_sink microphone_sink;
#if !defined(__APPLE__)
    lm_wav_sink system_sink;
#endif
};

static void lm_set_error(char* target, size_t size, const char* message)
{
    if (target == NULL || size == 0) return;
    if (message == NULL) message = "unknown native audio error";
    snprintf(target, size, "%s", message);
}

static void lm_set_ma_error(char* target, size_t size, const char* prefix, ma_result result)
{
    if (target == NULL || size == 0) return;
    snprintf(target, size, "%s: %s (%d)", prefix, ma_result_description(result), (int)result);
}

static FILE* lm_fopen_utf8(const char* path, const char* mode)
{
#if defined(_WIN32)
    wchar_t wide_path[32768];
    wchar_t wide_mode[16];
    if (MultiByteToWideChar(CP_UTF8, 0, path, -1, wide_path, 32768) <= 0) return NULL;
    if (MultiByteToWideChar(CP_UTF8, 0, mode, -1, wide_mode, 16) <= 0) return NULL;
    return _wfopen(wide_path, wide_mode);
#else
    return fopen(path, mode);
#endif
}

static void lm_write_u16(FILE* file, uint16_t value)
{
    unsigned char bytes[2] = {(unsigned char)(value & 0xff), (unsigned char)((value >> 8) & 0xff)};
    fwrite(bytes, 1, sizeof(bytes), file);
}

static void lm_write_u32(FILE* file, uint32_t value)
{
    unsigned char bytes[4] = {
        (unsigned char)(value & 0xff),
        (unsigned char)((value >> 8) & 0xff),
        (unsigned char)((value >> 16) & 0xff),
        (unsigned char)((value >> 24) & 0xff)
    };
    fwrite(bytes, 1, sizeof(bytes), file);
}

static int lm_wav_header(lm_wav_sink* sink)
{
    uint32_t data_size = sink->data_bytes > 0xffffffffULL ? 0xffffffffU : (uint32_t)sink->data_bytes;
    uint32_t byte_rate = sink->sample_rate * sink->channels * 2U;
    uint16_t block_align = (uint16_t)(sink->channels * 2U);
    if (fseek(sink->file, 0, SEEK_SET) != 0) return -1;
    fwrite("RIFF", 1, 4, sink->file);
    lm_write_u32(sink->file, 36U + data_size);
    fwrite("WAVEfmt ", 1, 8, sink->file);
    lm_write_u32(sink->file, 16);
    lm_write_u16(sink->file, 1);
    lm_write_u16(sink->file, sink->channels);
    lm_write_u32(sink->file, sink->sample_rate);
    lm_write_u32(sink->file, byte_rate);
    lm_write_u16(sink->file, block_align);
    lm_write_u16(sink->file, 16);
    fwrite("data", 1, 4, sink->file);
    lm_write_u32(sink->file, data_size);
    return ferror(sink->file) ? -1 : 0;
}

static int lm_wav_open(lm_wav_sink* sink, const char* path, unsigned int sample_rate, unsigned int channels)
{
    memset(sink, 0, sizeof(*sink));
    sink->sample_rate = sample_rate;
    sink->channels = (uint16_t)channels;
    sink->file = lm_fopen_utf8(path, "wb+");
    if (sink->file == NULL) return -1;
    setvbuf(sink->file, NULL, _IOFBF, 1024 * 1024);
    if (lm_wav_header(sink) != 0) {
        fclose(sink->file);
        sink->file = NULL;
        return -1;
    }
    if (fseek(sink->file, 44, SEEK_SET) != 0) {
        fclose(sink->file);
        sink->file = NULL;
        return -1;
    }
    return 0;
}

static int lm_wav_write(lm_wav_sink* sink, const void* data, size_t bytes)
{
    if (sink == NULL || sink->file == NULL || sink->failed) return -1;
    size_t written = fwrite(data, 1, bytes, sink->file);
    sink->data_bytes += written;
    if (written != bytes) {
        sink->failed = 1;
        return -1;
    }
    return 0;
}

static int lm_wav_close(lm_wav_sink* sink)
{
    if (sink == NULL || sink->file == NULL) return 0;
    int result = sink->failed ? -1 : lm_wav_header(sink);
    if (fflush(sink->file) != 0) result = -1;
    if (fclose(sink->file) != 0) result = -1;
    sink->file = NULL;
    return result;
}

/* Used by the ScreenCaptureKit translation unit. */
void* lm_platform_wav_open(const char* path, unsigned int sample_rate, unsigned int channels)
{
    lm_wav_sink* sink = (lm_wav_sink*)calloc(1, sizeof(lm_wav_sink));
    if (sink == NULL) return NULL;
    if (lm_wav_open(sink, path, sample_rate, channels) != 0) {
        free(sink);
        return NULL;
    }
    return sink;
}

int lm_platform_wav_write(void* opaque, const void* data, size_t bytes)
{
    return lm_wav_write((lm_wav_sink*)opaque, data, bytes);
}

int lm_platform_wav_close(void* opaque)
{
    if (opaque == NULL) return 0;
    lm_wav_sink* sink = (lm_wav_sink*)opaque;
    int result = lm_wav_close(sink);
    free(sink);
    return result;
}

static void lm_data_callback(ma_device* device, void* output, const void* input, ma_uint32 frame_count)
{
    (void)output;
    if (device == NULL || input == NULL) return;
    lm_wav_sink* sink = (lm_wav_sink*)device->pUserData;
    size_t bytes = (size_t)frame_count * sink->channels * sizeof(int16_t);
    (void)lm_wav_write(sink, input, bytes);
}

static ma_result lm_context_init(ma_context* context)
{
#if defined(_WIN32)
    ma_backend backends[] = {ma_backend_wasapi};
    return ma_context_init(backends, 1, NULL, context);
#elif defined(__APPLE__)
    ma_backend backends[] = {ma_backend_coreaudio};
    return ma_context_init(backends, 1, NULL, context);
#elif defined(__linux__)
    ma_backend backends[] = {ma_backend_pulseaudio, ma_backend_alsa};
    return ma_context_init(backends, 2, NULL, context);
#else
    return ma_context_init(NULL, 0, NULL, context);
#endif
}

static void lm_hex_encode(const unsigned char* data, size_t length, char* output, size_t output_size)
{
    static const char alphabet[] = "0123456789abcdef";
    if (output_size == 0) return;
    if (length * 2 + 1 > output_size) length = (output_size - 1) / 2;
    for (size_t i = 0; i < length; i++) {
        output[i * 2] = alphabet[(data[i] >> 4) & 0x0f];
        output[i * 2 + 1] = alphabet[data[i] & 0x0f];
    }
    output[length * 2] = '\0';
}

static int lm_hex_value(char value)
{
    if (value >= '0' && value <= '9') return value - '0';
    if (value >= 'a' && value <= 'f') return value - 'a' + 10;
    if (value >= 'A' && value <= 'F') return value - 'A' + 10;
    return -1;
}

static int lm_hex_decode(const char* input, ma_device_id* output)
{
    size_t input_length = strlen(input);
    if (input_length != sizeof(ma_device_id) * 2) return -1;
    unsigned char* bytes = (unsigned char*)output;
    for (size_t i = 0; i < sizeof(ma_device_id); i++) {
        int high = lm_hex_value(input[i * 2]);
        int low = lm_hex_value(input[i * 2 + 1]);
        if (high < 0 || low < 0) return -1;
        bytes[i] = (unsigned char)((high << 4) | low);
    }
    return 0;
}

static int lm_contains_monitor(const char* value)
{
    if (value == NULL) return 0;
    const char* needle = "monitor";
    size_t length = strlen(value);
    for (size_t i = 0; i + 7 <= length; i++) {
        size_t j = 0;
        for (; j < 7; j++) {
            if ((char)tolower((unsigned char)value[i + j]) != needle[j]) break;
        }
        if (j == 7) return 1;
    }
    return 0;
}

static int lm_is_null_device(const char* value)
{
    if (value == NULL) return 0;
    const char* marker = "discard all samples";
    size_t value_length = strlen(value);
    size_t marker_length = strlen(marker);
    for (size_t i = 0; i + marker_length <= value_length; i++) {
        size_t j = 0;
        for (; j < marker_length; j++) {
            if ((char)tolower((unsigned char)value[i + j]) != marker[j]) break;
        }
        if (j == marker_length) return 1;
    }
    return 0;
}

int lm_audio_list_devices(int kind, lm_device_info** devices, size_t* count, char* error_text, size_t error_size)
{
    if (devices == NULL || count == NULL) {
        lm_set_error(error_text, error_size, "invalid device list output");
        return -1;
    }
    *devices = NULL;
    *count = 0;

#if defined(__APPLE__)
    if (kind == LM_DEVICE_SYSTEM) {
        lm_device_info* result = (lm_device_info*)calloc(1, sizeof(lm_device_info));
        if (result == NULL) {
            lm_set_error(error_text, error_size, "out of memory");
            return -1;
        }
        snprintf(result[0].id, sizeof(result[0].id), "%s", "screencapturekit:system");
        snprintf(result[0].name, sizeof(result[0].name), "%s", "Системный звук macOS (ScreenCaptureKit)");
        result[0].is_default = 1;
        *devices = result;
        *count = 1;
        return 0;
    }
#endif

    ma_context context;
    ma_result init_result = lm_context_init(&context);
    if (init_result != MA_SUCCESS) {
        lm_set_ma_error(error_text, error_size, "cannot initialize native audio context", init_result);
        return -1;
    }
    ma_device_info* playback = NULL;
    ma_device_info* capture = NULL;
    ma_uint32 playback_count = 0;
    ma_uint32 capture_count = 0;
    ma_result list_result = ma_context_get_devices(&context, &playback, &playback_count, &capture, &capture_count);
    if (list_result != MA_SUCCESS) {
        ma_context_uninit(&context);
        lm_set_ma_error(error_text, error_size, "cannot enumerate native audio devices", list_result);
        return -1;
    }

    ma_device_info* source = capture;
    ma_uint32 source_count = capture_count;
#if defined(_WIN32)
    if (kind == LM_DEVICE_SYSTEM) {
        source = playback;
        source_count = playback_count;
    }
#endif
    size_t matching = 0;
    for (ma_uint32 i = 0; i < source_count; i++) {
        if (lm_is_null_device(source[i].name)) continue;
#if defined(__linux__)
        int monitor = lm_contains_monitor(source[i].name);
        if ((kind == LM_DEVICE_SYSTEM && !monitor) || (kind == LM_DEVICE_MICROPHONE && monitor)) continue;
#endif
        matching++;
    }
    lm_device_info* result = NULL;
    if (matching > 0) {
        result = (lm_device_info*)calloc(matching, sizeof(lm_device_info));
        if (result == NULL) {
            ma_context_uninit(&context);
            lm_set_error(error_text, error_size, "out of memory");
            return -1;
        }
    }
    size_t output_index = 0;
    for (ma_uint32 i = 0; i < source_count; i++) {
        if (lm_is_null_device(source[i].name)) continue;
#if defined(__linux__)
        int monitor = lm_contains_monitor(source[i].name);
        if ((kind == LM_DEVICE_SYSTEM && !monitor) || (kind == LM_DEVICE_MICROPHONE && monitor)) continue;
#endif
        lm_hex_encode((const unsigned char*)&source[i].id, sizeof(ma_device_id), result[output_index].id, sizeof(result[output_index].id));
        snprintf(result[output_index].name, sizeof(result[output_index].name), "%s", source[i].name);
        result[output_index].is_default = source[i].isDefault ? 1 : 0;
        output_index++;
    }
    ma_context_uninit(&context);
    *devices = result;
    *count = matching;
    return 0;
}

void lm_audio_free_devices(lm_device_info* devices)
{
    free(devices);
}

static int lm_init_microphone(lm_capture* capture, const char* device_id, unsigned int sample_rate, unsigned int channels, char* error_text, size_t error_size)
{
    ma_device_id id;
    ma_device_id* selected_id = NULL;
    if (device_id != NULL && device_id[0] != '\0') {
        if (lm_hex_decode(device_id, &id) != 0) {
            lm_set_error(error_text, error_size, "invalid microphone device id");
            return -1;
        }
        selected_id = &id;
    }
    ma_device_config config = ma_device_config_init(ma_device_type_capture);
    config.capture.pDeviceID = selected_id;
    config.capture.format = ma_format_s16;
    config.capture.channels = channels;
    config.sampleRate = sample_rate;
    config.periodSizeInMilliseconds = 100;
    config.dataCallback = lm_data_callback;
    config.pUserData = &capture->microphone_sink;
    ma_result result = ma_device_init(&capture->context, &config, &capture->microphone);
    if (result != MA_SUCCESS) {
        lm_set_ma_error(error_text, error_size, "cannot open microphone", result);
        return -1;
    }
    capture->microphone_initialized = 1;
    return 0;
}

#if !defined(__APPLE__)
static int lm_init_system(lm_capture* capture, const char* device_id, unsigned int sample_rate, unsigned int channels, char* error_text, size_t error_size)
{
    ma_device_id id;
    ma_device_id* selected_id = NULL;
    if (device_id != NULL && device_id[0] != '\0') {
        if (lm_hex_decode(device_id, &id) != 0) {
            lm_set_error(error_text, error_size, "invalid system audio device id");
            return -1;
        }
        selected_id = &id;
    }
#if defined(_WIN32)
    ma_device_config config = ma_device_config_init(ma_device_type_loopback);
#else
    ma_device_config config = ma_device_config_init(ma_device_type_capture);
#endif
    config.capture.pDeviceID = selected_id;
    config.capture.format = ma_format_s16;
    config.capture.channels = channels;
    config.sampleRate = sample_rate;
    config.periodSizeInMilliseconds = 100;
    config.dataCallback = lm_data_callback;
    config.pUserData = &capture->system_sink;
    ma_result result = ma_device_init(&capture->context, &config, &capture->system);
    if (result != MA_SUCCESS) {
        lm_set_ma_error(error_text, error_size, "cannot open system audio", result);
        return -1;
    }
    capture->system_initialized = 1;
    return 0;
}
#endif

static void lm_cleanup_capture(lm_capture* capture)
{
    if (capture == NULL) return;
#if defined(__APPLE__)
    if (capture->macos_system != NULL) {
        char ignored[256];
        (void)lm_macos_system_stop(capture->macos_system, ignored, sizeof(ignored));
        capture->macos_system = NULL;
    }
#else
    if (capture->system_started) ma_device_stop(&capture->system);
    if (capture->system_initialized) ma_device_uninit(&capture->system);
    capture->system_started = 0;
    capture->system_initialized = 0;
    (void)lm_wav_close(&capture->system_sink);
#endif
    if (capture->microphone_started) ma_device_stop(&capture->microphone);
    if (capture->microphone_initialized) ma_device_uninit(&capture->microphone);
    capture->microphone_started = 0;
    capture->microphone_initialized = 0;
    (void)lm_wav_close(&capture->microphone_sink);
    if (capture->context_initialized) ma_context_uninit(&capture->context);
    capture->context_initialized = 0;
}

lm_capture* lm_audio_start(
    const char* microphone_id,
    const char* system_id,
    const char* microphone_path,
    const char* system_path,
    unsigned int sample_rate,
    unsigned int channels,
    char* error_text,
    size_t error_size)
{
    if (microphone_path == NULL || system_path == NULL || sample_rate == 0 || channels == 0) {
        lm_set_error(error_text, error_size, "invalid native capture configuration");
        return NULL;
    }
    lm_capture* capture = (lm_capture*)calloc(1, sizeof(lm_capture));
    if (capture == NULL) {
        lm_set_error(error_text, error_size, "out of memory");
        return NULL;
    }
    ma_result context_result = lm_context_init(&capture->context);
    if (context_result != MA_SUCCESS) {
        lm_set_ma_error(error_text, error_size, "cannot initialize native audio context", context_result);
        free(capture);
        return NULL;
    }
    capture->context_initialized = 1;
    if (lm_wav_open(&capture->microphone_sink, microphone_path, sample_rate, channels) != 0) {
        lm_set_error(error_text, error_size, "cannot create microphone WAV");
        lm_cleanup_capture(capture);
        free(capture);
        return NULL;
    }
    if (lm_init_microphone(capture, microphone_id, sample_rate, channels, error_text, error_size) != 0) {
        lm_cleanup_capture(capture);
        free(capture);
        return NULL;
    }

#if defined(__APPLE__)
    if (system_id == NULL || strcmp(system_id, "screencapturekit:system") != 0) {
        lm_set_error(error_text, error_size, "invalid ScreenCaptureKit system source");
        lm_cleanup_capture(capture);
        free(capture);
        return NULL;
    }
    capture->macos_system = lm_macos_system_start(system_path, sample_rate, channels, error_text, error_size);
    if (capture->macos_system == NULL) {
        lm_cleanup_capture(capture);
        free(capture);
        return NULL;
    }
#else
    if (lm_wav_open(&capture->system_sink, system_path, sample_rate, channels) != 0) {
        lm_set_error(error_text, error_size, "cannot create system WAV");
        lm_cleanup_capture(capture);
        free(capture);
        return NULL;
    }
    if (lm_init_system(capture, system_id, sample_rate, channels, error_text, error_size) != 0) {
        lm_cleanup_capture(capture);
        free(capture);
        return NULL;
    }
#endif

    ma_result mic_start = ma_device_start(&capture->microphone);
    if (mic_start != MA_SUCCESS) {
        lm_set_ma_error(error_text, error_size, "cannot start microphone", mic_start);
        lm_cleanup_capture(capture);
        free(capture);
        return NULL;
    }
    capture->microphone_started = 1;

#if !defined(__APPLE__)
    ma_result system_start = ma_device_start(&capture->system);
    if (system_start != MA_SUCCESS) {
        lm_set_ma_error(error_text, error_size, "cannot start system audio", system_start);
        lm_cleanup_capture(capture);
        free(capture);
        return NULL;
    }
    capture->system_started = 1;
#endif
    return capture;
}

int lm_audio_stop(lm_capture* capture, char* error_text, size_t error_size)
{
    if (capture == NULL) {
        lm_set_error(error_text, error_size, "native capture is not active");
        return -1;
    }
    int failed = 0;
#if defined(__APPLE__)
    if (capture->macos_system != NULL) {
        if (lm_macos_system_stop(capture->macos_system, error_text, error_size) != 0) failed = 1;
        capture->macos_system = NULL;
    }
#else
    if (capture->system_started && ma_device_stop(&capture->system) != MA_SUCCESS) failed = 1;
    capture->system_started = 0;
    if (capture->system_initialized) ma_device_uninit(&capture->system);
    capture->system_initialized = 0;
    if (lm_wav_close(&capture->system_sink) != 0) failed = 1;
#endif
    if (capture->microphone_started && ma_device_stop(&capture->microphone) != MA_SUCCESS) failed = 1;
    capture->microphone_started = 0;
    if (capture->microphone_initialized) ma_device_uninit(&capture->microphone);
    capture->microphone_initialized = 0;
    if (lm_wav_close(&capture->microphone_sink) != 0) failed = 1;
    if (capture->context_initialized) ma_context_uninit(&capture->context);
    capture->context_initialized = 0;
    free(capture);
    if (failed) {
        if (error_text == NULL || error_text[0] == '\0') lm_set_error(error_text, error_size, "native audio finalization failed");
        return -1;
    }
    return 0;
}

const char* lm_audio_backend_name(void)
{
#if defined(_WIN32)
    return "WASAPI";
#elif defined(__APPLE__)
    return "CoreAudio + ScreenCaptureKit";
#elif defined(__linux__)
    return "PulseAudio/PipeWire + ALSA";
#else
    return "miniaudio";
#endif
}
