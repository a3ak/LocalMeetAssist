#ifndef LOCALMEETASSIST_LLAMA_BRIDGE_H
#define LOCALMEETASSIST_LLAMA_BRIDGE_H

#include <stdint.h>

typedef void* lm_llama_handle;

// lm_llama_probe loads the runtime and checks that every symbol the
// summarizer needs is exported. The model file is not touched.
int lm_llama_probe(const char* runtime_path);

// lm_llama_open loads the library, the GGUF model and creates a context.
lm_llama_handle lm_llama_open(const char* runtime_path,
                              const char* model_path,
                              int context_tokens,
                              int threads,
                              int gpu_layers);

// lm_llama_apply_chat formats a system/user pair with the chat template that
// ships inside the model file. Returns the prompt length, or the negative
// required length when the buffer is too small.
//
// thinking enables the reasoning preamble of a hybrid model such as Qwen3.5.
// Their chat template only opens the reasoning block when an optional variable
// is set, so thinking = 1 has to open it explicitly. Minutes want the direct
// answer: reasoning text conflicts with the "plain text, no markup" rule.
int lm_llama_apply_chat(lm_llama_handle handle,
                        const char* system_prompt,
                        const char* user_message,
                        int thinking,
                        char* buffer,
                        int capacity);

// lm_llama_generate writes at most capacity-1 bytes plus a terminator and
// returns the number of bytes written, or -1 on failure.
int lm_llama_generate(lm_llama_handle handle,
                      const char* prompt,
                      char* output,
                      int capacity,
                      int max_tokens,
                      float temperature,
                      float top_p,
                      int top_k);

// lm_llama_set_cancelled arms (1) or clears (0) the flag that the generation
// loop polls between tokens.
void lm_llama_set_cancelled(int32_t value);

// lm_llama_count_tokens reports how many tokens the model assigns to text, or
// -1 on failure. It lets the caller decide whether a transcript fits into the
// context before generation starts.
int lm_llama_count_tokens(lm_llama_handle handle, const char* text);

// lm_llama_context_tokens reports the context size that was actually created.
int lm_llama_context_tokens(lm_llama_handle handle);

void lm_llama_close(lm_llama_handle handle);
const char* lm_llama_last_error(void);

#endif
