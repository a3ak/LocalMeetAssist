#ifndef LOCALMEETASSIST_AEC3_BRIDGE_H
#define LOCALMEETASSIST_AEC3_BRIDGE_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef void* lm_aec3_handle;

lm_aec3_handle lm_aec3_create(int sample_rate, int delay_ms);
int lm_aec3_process(lm_aec3_handle handle, const int16_t* render,
                    const int16_t* capture, int16_t* output, int samples);
void lm_aec3_destroy(lm_aec3_handle handle);
const char* lm_aec3_last_error(void);

#ifdef __cplusplus
}
#endif
#endif
