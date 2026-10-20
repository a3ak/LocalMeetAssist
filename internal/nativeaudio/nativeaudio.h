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

// lm_audio_system_health reports whether the system capture stream is alive and
// recreates it after the output device disappeared (Windows loopback, Linux
// monitor source). Returns 0 while running, 1 when it was just restored, 2 when
// it is lost and cannot be restored.
int lm_audio_system_health(lm_capture* capture);

// Микрофон на macOS: статус доступа (0 не определён, 1 ограничен, 2 запрещён,
// 3 разрешён) и запрос доступа, если бандл объясняет причину. Запрос возвращает
// -1, когда спросить нельзя (обычный бинарник без Info.plist).
int lm_macos_microphone_status(void);
int lm_macos_microphone_request(void);
int lm_audio_activity(int* microphone_active, int* system_active, char* error_text, size_t error_size);
int lm_audio_active_input_app(char* name, size_t name_size, int* found, char* error_text, size_t error_size);
const char* lm_audio_backend_name(void);

#ifdef __cplusplus
}
#endif

#endif
