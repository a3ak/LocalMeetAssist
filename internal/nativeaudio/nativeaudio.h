#ifndef LOCALMEETASSIST_NATIVE_AUDIO_H
#define LOCALMEETASSIST_NATIVE_AUDIO_H

#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

#define LM_DEVICE_ID_MAX 1024
#define LM_DEVICE_NAME_MAX 512

typedef struct lm_capture lm_capture;

typedef struct {
    char id[LM_DEVICE_ID_MAX];
    char name[LM_DEVICE_NAME_MAX];
    int is_default;
} lm_device_info;

enum {
    LM_DEVICE_MICROPHONE = 1,
    LM_DEVICE_SYSTEM = 2
};

int lm_audio_list_devices(int kind, lm_device_info** devices, size_t* count, char* error_text, size_t error_size);
void lm_audio_free_devices(lm_device_info* devices);

lm_capture* lm_audio_start(
    const char* microphone_id,
    const char* system_id,
    const char* microphone_path,
    const char* system_path,
    unsigned int sample_rate,
    unsigned int channels,
    char* error_text,
    size_t error_size
);

int lm_audio_stop(lm_capture* capture, char* error_text, size_t error_size);
const char* lm_audio_backend_name(void);

#ifdef __cplusplus
}
#endif

#endif
