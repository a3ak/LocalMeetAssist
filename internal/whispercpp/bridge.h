#ifndef LOCALMEETASSIST_WHISPER_BRIDGE_H
#define LOCALMEETASSIST_WHISPER_BRIDGE_H

#include <stdint.h>

typedef void* lm_whisper_handle;

int lm_whisper_probe(const char* runtime_path);
lm_whisper_handle lm_whisper_open(const char* runtime_path,
                                  const char* model_path,
                                  const char* language, int threads);
int lm_whisper_run(lm_whisper_handle handle, const float* samples, int count);
int lm_whisper_segment_count(lm_whisper_handle handle);
int64_t lm_whisper_segment_start(lm_whisper_handle handle, int index);
int64_t lm_whisper_segment_end(lm_whisper_handle handle, int index);
const char* lm_whisper_segment_text(lm_whisper_handle handle, int index);
void lm_whisper_close(lm_whisper_handle handle);
const char* lm_whisper_last_error(void);

#endif
