// Bridge to the llama.cpp native runtime.
//
// The library is not linked at build time: it is downloaded on first use and
// loaded here with dlopen/LoadLibrary, exactly like the whisper.cpp runtime.
// Only this translation unit knows the exported symbol names, so a runtime
// that lacks one of them fails with a readable message instead of a linker
// error. The header llama.h is kept at the tag of the downloaded release, so
// the parameter structs passed by value have the layout the runtime expects.
#include "bridge.h"
#include "llama.h"

#include <stdio.h>
#include <stdlib.h>
#include <stdatomic.h>
#include <string.h>

#ifdef _WIN32
#include <windows.h>
typedef HMODULE lm_library;
#define LM_DLSYM(lib, name) GetProcAddress((lib), (name))
#define LM_DLCLOSE(lib) FreeLibrary((lib))
#else
#include <dlfcn.h>
typedef void* lm_library;
#define LM_DLSYM(lib, name) dlsym((lib), (name))
#define LM_DLCLOSE(lib) dlclose((lib))
#endif

typedef struct llama_model_params (*fn_model_default_params)(void);
typedef struct llama_context_params (*fn_context_default_params)(void);
typedef struct llama_model* (*fn_model_load)(const char*, struct llama_model_params);
typedef void (*fn_model_free)(struct llama_model*);
typedef struct llama_context* (*fn_init_from_model)(struct llama_model*, struct llama_context_params);
typedef void (*fn_free)(struct llama_context*);
typedef void (*fn_backend_init)(void);
typedef void (*fn_load_all)(void);
typedef void (*fn_log_set)(ggml_log_callback, void*);
typedef const struct llama_vocab* (*fn_get_vocab)(const struct llama_model*);
typedef int32_t (*fn_vocab_n_tokens)(const struct llama_vocab*);
typedef bool (*fn_vocab_is_eog)(const struct llama_vocab*, llama_token);
typedef int32_t (*fn_tokenize)(const struct llama_vocab*, const char*, int32_t, llama_token*, int32_t, bool, bool);
typedef int32_t (*fn_token_to_piece)(const struct llama_vocab*, llama_token, char*, int32_t, int32_t, bool);
typedef const char* (*fn_chat_template)(const struct llama_model*, const char*);
typedef int32_t (*fn_chat_apply)(const char*, const struct llama_chat_message*, size_t, bool, char*, int32_t);
typedef struct llama_batch (*fn_batch_init)(int32_t, int32_t, int32_t);
typedef void (*fn_batch_free)(struct llama_batch);
typedef int32_t (*fn_decode)(struct llama_context*, struct llama_batch);
typedef void (*fn_memory_clear)(llama_memory_t, bool);
typedef llama_memory_t (*fn_get_memory)(const struct llama_context*);
typedef void (*fn_set_n_threads)(struct llama_context*, int32_t, int32_t);
typedef struct llama_sampler_chain_params (*fn_chain_default_params)(void);
typedef struct llama_sampler* (*fn_chain_init)(struct llama_sampler_chain_params);
typedef void (*fn_chain_add)(struct llama_sampler*, struct llama_sampler*);
typedef struct llama_sampler* (*fn_sampler_greedy)(void);
typedef struct llama_sampler* (*fn_sampler_dist)(uint32_t);
typedef struct llama_sampler* (*fn_sampler_top_k)(int32_t);
typedef struct llama_sampler* (*fn_sampler_top_p)(float, size_t);
typedef struct llama_sampler* (*fn_sampler_temp)(float);
typedef struct llama_sampler* (*fn_sampler_penalties)(int32_t, int32_t, float, float, float);
typedef llama_token (*fn_sampler_sample)(struct llama_sampler*, struct llama_context*, int32_t);
typedef void (*fn_sampler_free)(struct llama_sampler*);

struct lm_llama_state {
  lm_library library;
  struct llama_model* model;
  struct llama_context* context;
  const struct llama_vocab* vocab;
  int context_tokens;
  int threads;
  int batch_tokens;
  int vocab_size;
  const char* chat_template;
  fn_model_free model_free;
  fn_free context_free;
  fn_vocab_is_eog vocab_is_eog;
  fn_tokenize tokenize;
  fn_token_to_piece token_to_piece;
  fn_chat_apply chat_apply;
  fn_batch_init batch_init;
  fn_batch_free batch_free;
  fn_decode decode;
  fn_memory_clear memory_clear;
  fn_get_memory get_memory;
  fn_chain_init chain_init;
  fn_chain_add chain_add;
  fn_sampler_greedy sampler_greedy;
  fn_sampler_dist sampler_dist;
  fn_sampler_top_k sampler_top_k;
  fn_sampler_top_p sampler_top_p;
  fn_sampler_temp sampler_temp;
  fn_sampler_penalties sampler_penalties;
  fn_sampler_sample sampler_sample;
  fn_sampler_free sampler_free;
};

static _Thread_local char last_error[1024];
static int backend_ready = 0;

// Cancellation flag polled between generated tokens. It is written from the
// goroutine that watches the request context, so the access has to be atomic.
static _Atomic int32_t lm_llama_cancelled = 0;

void lm_llama_set_cancelled(int32_t value) {
  atomic_store(&lm_llama_cancelled, value ? 1 : 0);
}

static int lm_llama_abort_check(void) {
  return atomic_load(&lm_llama_cancelled) != 0;
}

static void set_error(const char* value) {
  snprintf(last_error, sizeof(last_error), "%s", value ? value : "unknown llama.cpp error");
}

// The runtime is talkative: it prints backend and model loading progress to
// stderr. A desktop application must not have that in its log, so the log
// callback is replaced before any model is loaded.
static void discard_log(enum ggml_log_level level, const char* text, void* user_data) {
  (void) level;
  (void) text;
  (void) user_data;
}

static void runtime_directory(const char* path, char* output, size_t size) {
  snprintf(output, size, "%s", path);
  char* slash = strrchr(output, '/');
#ifdef _WIN32
  char* backslash = strrchr(output, '\\');
  if (!slash || (backslash && backslash > slash)) slash = backslash;
#endif
  if (slash) *slash = '\0';
}

static lm_library open_library(const char* path) {
#ifdef _WIN32
  // Windows resolves llama.dll's dependencies (ggml.dll, ggml-base.dll,
  // ggml-cpu.dll, libomp.dll) through the classic search order, so the
  // directory of the library has to be added explicitly.
  char directory[2048];
  runtime_directory(path, directory, sizeof(directory));
  SetDllDirectoryA(directory);
  lm_library result = LoadLibraryA(path);
  if (!result) set_error("cannot load llama.dll or one of its dependencies");
  return result;
#else
  // On macOS and Linux the library finds its neighbours through its own
  // @rpath/$ORIGIN, so dlopen is enough.
  lm_library result = dlopen(path, RTLD_NOW | RTLD_GLOBAL);
  if (!result) set_error(dlerror());
  return result;
#endif
}

#define LOAD_REQUIRED(state, field, type, symbol) do { \
  (state)->field = (type) LM_DLSYM((state)->library, (symbol)); \
  if (!(state)->field) { set_error("llama.cpp runtime misses required symbol " symbol); goto fail; } \
} while (0)

#define LOAD_OPTIONAL(state, field, type, symbol) \
  (state)->field = (type) LM_DLSYM((state)->library, (symbol))

// Required symbols of the generation path. Kept in one place so the probe and
// the real open cannot drift apart.
static const char* const lm_llama_required_symbols[] = {
    "llama_backend_init", "llama_model_default_params", "llama_context_default_params",
    "llama_model_load_from_file", "llama_model_free", "llama_init_from_model", "llama_free",
    "llama_model_get_vocab", "llama_vocab_n_tokens", "llama_vocab_is_eog", "llama_tokenize",
    "llama_token_to_piece", "llama_chat_apply_template", "llama_batch_init", "llama_batch_free",
    "llama_decode", "llama_memory_clear", "llama_get_memory", "llama_sampler_chain_init",
    "llama_sampler_chain_add", "llama_sampler_sample", "llama_sampler_free",
    "llama_sampler_init_greedy", "llama_sampler_init_dist", "llama_sampler_init_top_k",
    "llama_sampler_init_top_p", "llama_sampler_init_temp",
};

int lm_llama_probe(const char* runtime_path) {
  last_error[0] = '\0';
  lm_library library = open_library(runtime_path);
  if (!library) return -1;
  for (size_t i = 0; i < sizeof(lm_llama_required_symbols) / sizeof(lm_llama_required_symbols[0]); ++i) {
    if (!LM_DLSYM(library, lm_llama_required_symbols[i])) {
      char message[256];
      snprintf(message, sizeof(message), "llama.cpp runtime misses required symbol %s", lm_llama_required_symbols[i]);
      set_error(message);
      LM_DLCLOSE(library);
      return -1;
    }
  }
  // The sampler chain options are passed by value too; a runtime built from a
  // different llama.h would silently misread them, so it is checked as well.
  if (!LM_DLSYM(library, "llama_sampler_chain_default_params")) {
    set_error("llama.cpp runtime misses required symbol llama_sampler_chain_default_params");
    LM_DLCLOSE(library);
    return -1;
  }
  LM_DLCLOSE(library);
  return 0;
}

lm_llama_handle lm_llama_open(const char* runtime_path,
                              const char* model_path,
                              int context_tokens,
                              int threads,
                              int gpu_layers) {
  last_error[0] = '\0';
  struct lm_llama_state* state = calloc(1, sizeof(*state));
  if (!state) {
    set_error("out of memory");
    return NULL;
  }
  state->library = open_library(runtime_path);
  if (!state->library) goto fail;

  fn_backend_init backend_init = NULL;
  LOAD_REQUIRED(state, model_free, fn_model_free, "llama_model_free");
  LOAD_REQUIRED(state, context_free, fn_free, "llama_free");
  LOAD_REQUIRED(state, vocab_is_eog, fn_vocab_is_eog, "llama_vocab_is_eog");
  LOAD_REQUIRED(state, tokenize, fn_tokenize, "llama_tokenize");
  LOAD_REQUIRED(state, token_to_piece, fn_token_to_piece, "llama_token_to_piece");
  LOAD_REQUIRED(state, chat_apply, fn_chat_apply, "llama_chat_apply_template");
  LOAD_REQUIRED(state, batch_init, fn_batch_init, "llama_batch_init");
  LOAD_REQUIRED(state, batch_free, fn_batch_free, "llama_batch_free");
  LOAD_REQUIRED(state, decode, fn_decode, "llama_decode");
  LOAD_REQUIRED(state, memory_clear, fn_memory_clear, "llama_memory_clear");
  LOAD_REQUIRED(state, get_memory, fn_get_memory, "llama_get_memory");
  LOAD_REQUIRED(state, chain_init, fn_chain_init, "llama_sampler_chain_init");
  LOAD_REQUIRED(state, chain_add, fn_chain_add, "llama_sampler_chain_add");
  LOAD_REQUIRED(state, sampler_greedy, fn_sampler_greedy, "llama_sampler_init_greedy");
  LOAD_REQUIRED(state, sampler_dist, fn_sampler_dist, "llama_sampler_init_dist");
  LOAD_REQUIRED(state, sampler_top_k, fn_sampler_top_k, "llama_sampler_init_top_k");
  LOAD_REQUIRED(state, sampler_top_p, fn_sampler_top_p, "llama_sampler_init_top_p");
  LOAD_REQUIRED(state, sampler_temp, fn_sampler_temp, "llama_sampler_init_temp");
  LOAD_REQUIRED(state, sampler_sample, fn_sampler_sample, "llama_sampler_sample");
  LOAD_REQUIRED(state, sampler_free, fn_sampler_free, "llama_sampler_free");
  LOAD_OPTIONAL(state, sampler_penalties, fn_sampler_penalties, "llama_sampler_init_penalties");

  fn_model_default_params model_default_params = (fn_model_default_params) LM_DLSYM(state->library, "llama_model_default_params");
  fn_context_default_params context_default_params = (fn_context_default_params) LM_DLSYM(state->library, "llama_context_default_params");
  fn_model_load model_load = (fn_model_load) LM_DLSYM(state->library, "llama_model_load_from_file");
  fn_init_from_model init_from_model = (fn_init_from_model) LM_DLSYM(state->library, "llama_init_from_model");
  fn_get_vocab get_vocab = (fn_get_vocab) LM_DLSYM(state->library, "llama_model_get_vocab");
  fn_vocab_n_tokens vocab_n_tokens = (fn_vocab_n_tokens) LM_DLSYM(state->library, "llama_vocab_n_tokens");
  backend_init = (fn_backend_init) LM_DLSYM(state->library, "llama_backend_init");
  if (!model_default_params || !context_default_params || !model_load || !init_from_model ||
      !get_vocab || !vocab_n_tokens || !backend_init) {
    set_error("llama.cpp runtime cannot initialize a model");
    goto fail;
  }

  // The runtime logs on stderr by itself; a desktop application keeps its log
  // clean and reports problems through lm_llama_last_error instead.
  fn_log_set log_set = (fn_log_set) LM_DLSYM(state->library, "llama_log_set");
  if (log_set) log_set(discard_log, NULL);
  if (!backend_ready) {
    fn_load_all load_all = (fn_load_all) LM_DLSYM(state->library, "ggml_backend_load_all");
    if (load_all) load_all();
    backend_init();
    backend_ready = 1;
  }

  struct llama_model_params model_params = model_default_params();
  // A negative value offloads every layer that fits into the accelerator: on
  // macOS that is Metal, on the Vulkan build it is the GPU.
  model_params.n_gpu_layers = gpu_layers;
  state->model = model_load(model_path, model_params);
  if (!state->model) {
    set_error("llama.cpp could not load the selected GGUF model");
    goto fail;
  }
  state->vocab = get_vocab(state->model);
  state->vocab_size = vocab_n_tokens(state->vocab);

  struct llama_context_params context_params = context_default_params();
  context_params.n_ctx = context_tokens > 0 ? (uint32_t) context_tokens : 8192;
  context_params.n_batch = context_params.n_ctx < 2048 ? context_params.n_ctx : 2048;
  context_params.n_ubatch = context_params.n_batch;
  context_params.n_threads = threads > 0 ? threads : 4;
  context_params.n_threads_batch = context_params.n_threads;
  state->context = init_from_model(state->model, context_params);
  if (!state->context) {
    set_error("llama.cpp could not create a context; try a smaller context size");
    goto fail;
  }
  state->context_tokens = (int) context_params.n_ctx;
  state->batch_tokens = (int) context_params.n_batch;
  state->threads = context_params.n_threads;
  fn_chat_template chat_template = (fn_chat_template) LM_DLSYM(state->library, "llama_model_chat_template");
  if (chat_template) state->chat_template = chat_template(state->model, NULL);
  return state;

fail:
  if (state->context && state->context_free) state->context_free(state->context);
  if (state->model && state->model_free) state->model_free(state->model);
  if (state->library) LM_DLCLOSE(state->library);
  free(state);
  return NULL;
}

// The generation prompt of the Qwen3.5 chat template: the assistant header the
// model answers from, followed by its reasoning block. Leaving that block open
// makes the model write a reasoning preamble instead of the minutes, so it is
// closed immediately. A model whose template does not use this exact header
// keeps the prompt the template produced.
static const char assistant_header[] = "<|im_start|>assistant\n";
static const char reasoning_closed[] = "<think>\n\n</think>\n\n";
static const char reasoning_open[] = "<think>\n";

// strip_generation_prompt removes the assistant header that was rendered with
// add_ass, so the header can be written again with the reasoning block closed.
// Returns the new length, or -1 when the prompt does not end with the header.
static int32_t strip_generation_prompt(char* buffer, int32_t length) {
  const int32_t header_length = (int32_t) (sizeof(assistant_header) - 1);
  if (length < header_length) return -1;
  if (memcmp(buffer + length - header_length, assistant_header, (size_t) header_length) != 0) return -1;
  return length - header_length;
}

// lm_llama_apply_chat formats the system/user pair with the chat template of
// the model. The reasoning block of a hybrid model such as Qwen3.5 is closed
// immediately unless thinking is requested, because the model writes a
// reasoning preamble instead of the minutes when that block stays open.
int lm_llama_apply_chat(lm_llama_handle opaque,
                        const char* system_prompt,
                        const char* user_message,
                        int thinking,
                        char* buffer,
                        int capacity) {
  struct lm_llama_state* state = (struct lm_llama_state*) opaque;
  if (!state || !buffer || capacity <= 0) {
    set_error("invalid llama.cpp chat request");
    return -1;
  }
  // llama_chat_apply_template accepts a NULL template and then falls back to
  // the template stored in the model, so a model without one is not fatal.
  const char* template_value = state->chat_template ? state->chat_template : NULL;
  struct llama_chat_message single[1];
  struct llama_chat_message pair[2];
  struct llama_chat_message* messages = single;
  size_t count = 1;
  if (system_prompt && system_prompt[0]) {
    pair[0].role = "system";
    pair[0].content = system_prompt;
    pair[1].role = "user";
    pair[1].content = user_message ? user_message : "";
    messages = pair;
    count = 2;
  } else {
    single[0].role = "user";
    single[0].content = user_message ? user_message : "";
  }
  // Ask for the required size first: the same call with an empty buffer
  // returns how much room the formatted prompt needs, which keeps the write
  // below inside the buffer the caller allocated.
  int32_t need = state->chat_apply(template_value, messages, count, true, NULL, 0);
  if (need < 0) {
    set_error("llama.cpp could not apply the chat template of the model");
    return -1;
  }
  if (need > capacity - 1) return (int) need + 1;
  int32_t written = state->chat_apply(template_value, messages, count, true, buffer, capacity);
  if (written < 0) {
    set_error("llama.cpp could not apply the chat template of the model");
    return -1;
  }
  // Only a template that emits this exact generation prompt is adjusted; any
  // other model keeps the prompt its own template produced.
  if (!state->chat_template) return (int) written;
  int32_t stripped = strip_generation_prompt(buffer, written);
  if (stripped < 0) return (int) written;
  const char* reasoning = thinking ? reasoning_open : reasoning_closed;
  int32_t reasoning_length = (int32_t) (thinking ? sizeof(reasoning_open) - 1 : sizeof(reasoning_closed) - 1);
  int32_t header_length = (int32_t) (sizeof(assistant_header) - 1);
  if (stripped + header_length + reasoning_length > capacity - 1) return stripped + header_length + reasoning_length + 1;
  memcpy(buffer + stripped, assistant_header, (size_t) header_length);
  memcpy(buffer + stripped + header_length, reasoning, (size_t) reasoning_length);
  buffer[stripped + header_length + reasoning_length] = '\0';
  return stripped + header_length + reasoning_length;
}

// Tokenize text into a freshly allocated array. Returns the count, or -1 when
// the text cannot be tokenized.
static int tokenize_text(struct lm_llama_state* state, const char* text, int text_length,
                         bool add_special, llama_token** out) {
  *out = NULL;
  // The runtime returns the needed size as a negative number when the buffer
  // is too small, so one retry with the reported size is enough.
  int32_t capacity = text_length + 8;
  llama_token* tokens = (llama_token*) malloc(sizeof(llama_token) * (size_t) capacity);
  if (!tokens) {
    set_error("out of memory");
    return -1;
  }
  int32_t count = state->tokenize(state->vocab, text, text_length, tokens, capacity, add_special, true);
  if (count < 0) {
    free(tokens);
    capacity = -count + 8;
    tokens = (llama_token*) malloc(sizeof(llama_token) * (size_t) capacity);
    if (!tokens) {
      set_error("out of memory");
      return -1;
    }
    count = state->tokenize(state->vocab, text, text_length, tokens, capacity, add_special, true);
  }
  if (count <= 0) {
    free(tokens);
    set_error("llama.cpp could not tokenize the prompt");
    return -1;
  }
  *out = tokens;
  return (int) count;
}

// Decode tokens in batches of at most state->batch_tokens. Logits are produced
// only for the last token, which is what sampling needs.
static int decode_tokens(struct lm_llama_state* state, llama_token* tokens, int count, int start_position) {
  int offset = 0;
  while (offset < count) {
    int chunk = count - offset;
    if (chunk > state->batch_tokens) chunk = state->batch_tokens;
    struct llama_batch batch = state->batch_init(chunk, 0, 1);
    if (!batch.token) {
      set_error("llama.cpp could not allocate a batch");
      return -1;
    }
    for (int i = 0; i < chunk; ++i) {
      batch.token[i] = tokens[offset + i];
      batch.pos[i] = start_position + offset + i;
      batch.n_seq_id[i] = 1;
      batch.seq_id[i][0] = 0;
      batch.logits[i] = (i == chunk - 1) ? 1 : 0;
    }
    batch.n_tokens = chunk;
    int32_t result = state->decode(state->context, batch);
    state->batch_free(batch);
    if (result != 0) {
      set_error("llama.cpp decoding failed");
      return -1;
    }
    offset += chunk;
  }
  return 0;
}

// Build the sampler for one request. Greedy output keeps minutes deterministic;
// a positive temperature is used when the caller asks for sampling.
static struct llama_sampler* build_sampler(struct lm_llama_state* state, float temperature, float top_p, int top_k) {
  if (temperature <= 0.0f) {
    return state->sampler_greedy();
  }
  struct llama_sampler* chain = state->chain_init(
      ((fn_chain_default_params) LM_DLSYM(state->library, "llama_sampler_chain_default_params"))());
  if (!chain) return NULL;
  if (state->sampler_penalties && state->vocab_size > 0) {
    state->chain_add(chain, state->sampler_penalties(state->vocab_size, 64, 1.1f, 0.0f, 0.0f));
  }
  if (top_k > 0) state->chain_add(chain, state->sampler_top_k(top_k));
  if (top_p > 0.0f && top_p < 1.0f) state->chain_add(chain, state->sampler_top_p(top_p, 1));
  state->chain_add(chain, state->sampler_temp(temperature));
  state->chain_add(chain, state->sampler_dist(LLAMA_DEFAULT_SEED));
  return chain;
}

int lm_llama_generate(lm_llama_handle opaque,
                      const char* prompt,
                      char* output,
                      int capacity,
                      int max_tokens,
                      float temperature,
                      float top_p,
                      int top_k) {
  struct lm_llama_state* state = (struct lm_llama_state*) opaque;
  if (!state || !prompt || !output || capacity <= 0) {
    set_error("invalid llama.cpp generation request");
    return -1;
  }
  output[0] = '\0';
  if (max_tokens <= 0) max_tokens = 512;
  size_t prompt_length = strlen(prompt);
  llama_token* tokens = NULL;
  int count = tokenize_text(state, prompt, (int) prompt_length, true, &tokens);
  if (count < 0) return -1;
  if (count >= state->context_tokens) {
    free(tokens);
    set_error("the prompt does not fit into the model context; raise summary.context_tokens or shorten the transcript");
    return -1;
  }
  struct llama_sampler* sampler = build_sampler(state, temperature, top_p, top_k);
  if (!sampler) {
    free(tokens);
    set_error("llama.cpp could not create a sampler");
    return -1;
  }
  // Every request starts from an empty cache: one meeting is summarized at a
  // time and a stale cache would silently corrupt the answer.
  state->memory_clear(state->get_memory(state->context), true);
  int failure = decode_tokens(state, tokens, count, 0);
  free(tokens);
  if (failure != 0) {
    state->sampler_free(sampler);
    return -1;
  }
  int written = 0;
  int position = count;
  for (int step = 0; step < max_tokens; ++step) {
    if (lm_llama_abort_check()) {
      set_error("cancelled");
      state->sampler_free(sampler);
      return -1;
    }
    llama_token token = state->sampler_sample(sampler, state->context, -1);
    if (state->vocab_is_eog(state->vocab, token)) break;
    char piece[256];
    int32_t piece_length = state->token_to_piece(state->vocab, token, piece, (int32_t) sizeof(piece), 0, false);
    if (piece_length < 0) break;
    if (piece_length > (int32_t) sizeof(piece)) piece_length = (int32_t) sizeof(piece);
    if (written + piece_length >= capacity - 1) {
      written += piece_length;
      break;
    }
    memcpy(output + written, piece, (size_t) piece_length);
    written += (int) piece_length;
    output[written] = '\0';
    if (decode_tokens(state, &token, 1, position) != 0) {
      state->sampler_free(sampler);
      return -1;
    }
    position += 1;
  }
  state->sampler_free(sampler);
  return written;
}

int lm_llama_count_tokens(lm_llama_handle opaque, const char* text) {
  struct lm_llama_state* state = (struct lm_llama_state*) opaque;
  if (!state || !text) {
    set_error("invalid llama.cpp token request");
    return -1;
  }
  llama_token* tokens = NULL;
  int count = tokenize_text(state, text, (int) strlen(text), false, &tokens);
  free(tokens);
  return count;
}

int lm_llama_context_tokens(lm_llama_handle opaque) {
  struct lm_llama_state* state = (struct lm_llama_state*) opaque;
  return state ? state->context_tokens : 0;
}

void lm_llama_close(lm_llama_handle opaque) {
  struct lm_llama_state* state = (struct lm_llama_state*) opaque;
  if (!state) return;
  if (state->context) state->context_free(state->context);
  if (state->model) state->model_free(state->model);
  if (state->library) LM_DLCLOSE(state->library);
  free(state);
}

const char* lm_llama_last_error(void) { return last_error; }
