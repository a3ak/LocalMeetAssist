#include "bridge.h"
#include "whisper.h"

#include <stdio.h>
#include <stdlib.h>
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

typedef struct whisper_context_params (*fn_context_params)(void);
typedef struct whisper_context* (*fn_init)(const char*, struct whisper_context_params);
typedef void (*fn_free)(struct whisper_context*);
typedef struct whisper_full_params (*fn_full_params)(enum whisper_sampling_strategy);
typedef int (*fn_full)(struct whisper_context*, struct whisper_full_params, const float*, int);
typedef int (*fn_count)(struct whisper_context*);
typedef int64_t (*fn_time)(struct whisper_context*, int);
typedef const char* (*fn_text)(struct whisper_context*, int);
typedef void (*fn_load_backends)(const char*);

struct lm_whisper_state {
  lm_library library;
  struct whisper_context* context;
  char* language;
  int threads;
  fn_free free_context;
  fn_full_params default_full_params;
  fn_full full;
  fn_count count;
  fn_time start;
  fn_time end;
  fn_text text;
};

static _Thread_local char last_error[1024];

static void set_error(const char* value) {
  snprintf(last_error, sizeof(last_error), "%s", value ? value : "unknown whisper.cpp error");
}

static char* duplicate_string(const char* value) {
  size_t length = strlen(value) + 1;
  char* copy = (char*) malloc(length);
  if (copy) memcpy(copy, value, length);
  return copy;
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
  char directory[2048];
  runtime_directory(path, directory, sizeof(directory));
  SetDllDirectoryA(directory);
  lm_library result = LoadLibraryA(path);
  if (!result) set_error("cannot load whisper.dll or one of its dependencies");
  return result;
#else
  lm_library result = dlopen(path, RTLD_NOW | RTLD_GLOBAL);
  if (!result) set_error(dlerror());
  return result;
#endif
}

#define LOAD_REQUIRED(state, field, type, symbol) do { \
  (state)->field = (type) LM_DLSYM((state)->library, (symbol)); \
  if (!(state)->field) { set_error("whisper.cpp runtime misses required symbol " symbol); goto fail; } \
} while (0)

int lm_whisper_probe(const char* runtime_path) {
  last_error[0] = '\0';
  lm_library library = open_library(runtime_path);
  if (!library) return -1;
  const char* symbols[] = {
      "whisper_context_default_params", "whisper_init_from_file_with_params",
      "whisper_free", "whisper_full_default_params", "whisper_full",
      "whisper_full_n_segments", "whisper_full_get_segment_text"};
  for (size_t i = 0; i < sizeof(symbols) / sizeof(symbols[0]); ++i) {
    if (!LM_DLSYM(library, symbols[i])) {
      char message[256];
      snprintf(message, sizeof(message), "whisper.cpp runtime misses required symbol %s", symbols[i]);
      set_error(message);
      LM_DLCLOSE(library);
      return -1;
    }
  }
  LM_DLCLOSE(library);
  return 0;
}

lm_whisper_handle lm_whisper_open(const char* runtime_path,
                                  const char* model_path,
                                  const char* language, int threads) {
  last_error[0] = '\0';
  struct lm_whisper_state* state = calloc(1, sizeof(*state));
  if (!state) {
    set_error("out of memory");
    return NULL;
  }
  state->library = open_library(runtime_path);
  if (!state->library) goto fail;
  fn_context_params context_params = NULL;
  fn_init init = NULL;
  LOAD_REQUIRED(state, free_context, fn_free, "whisper_free");
  LOAD_REQUIRED(state, default_full_params, fn_full_params, "whisper_full_default_params");
  LOAD_REQUIRED(state, full, fn_full, "whisper_full");
  LOAD_REQUIRED(state, count, fn_count, "whisper_full_n_segments");
  LOAD_REQUIRED(state, start, fn_time, "whisper_full_get_segment_t0");
  LOAD_REQUIRED(state, end, fn_time, "whisper_full_get_segment_t1");
  LOAD_REQUIRED(state, text, fn_text, "whisper_full_get_segment_text");
  context_params = (fn_context_params) LM_DLSYM(state->library, "whisper_context_default_params");
  init = (fn_init) LM_DLSYM(state->library, "whisper_init_from_file_with_params");
  if (!context_params || !init) {
    set_error("whisper.cpp runtime cannot initialize a model");
    goto fail;
  }
  char directory[2048];
  runtime_directory(runtime_path, directory, sizeof(directory));
  fn_load_backends load_backends = (fn_load_backends) LM_DLSYM(state->library, "ggml_backend_load_all_from_path");
  if (load_backends) load_backends(directory);
  state->context = init(model_path, context_params());
  if (!state->context) {
    set_error("whisper.cpp could not load the selected GGML model");
    goto fail;
  }
  state->language = duplicate_string(language && language[0] ? language : "ru");
  if (!state->language) {
    set_error("out of memory");
    goto fail;
  }
  state->threads = threads > 0 ? threads : 4;
  return state;

fail:
  if (state->context && state->free_context) state->free_context(state->context);
  if (state->library) LM_DLCLOSE(state->library);
  free(state->language);
  free(state);
  return NULL;
}

int lm_whisper_run(lm_whisper_handle opaque, const float* samples, int count) {
  struct lm_whisper_state* state = (struct lm_whisper_state*) opaque;
  if (!state || !samples || count <= 0) {
    set_error("invalid whisper.cpp audio buffer");
    return -1;
  }
  struct whisper_full_params params = state->default_full_params(WHISPER_SAMPLING_GREEDY);
  params.n_threads = state->threads;
  params.language = state->language;
  params.translate = false;
  params.no_context = true;
  params.print_realtime = false;
  params.print_progress = false;
  params.print_timestamps = false;
  params.print_special = false;
  params.suppress_blank = true;
  params.suppress_nst = true;
  int result = state->full(state->context, params, samples, count);
  if (result != 0) set_error("whisper.cpp inference failed");
  return result;
}

int lm_whisper_segment_count(lm_whisper_handle opaque) {
  struct lm_whisper_state* state = (struct lm_whisper_state*) opaque;
  return state ? state->count(state->context) : 0;
}

int64_t lm_whisper_segment_start(lm_whisper_handle opaque, int index) {
  struct lm_whisper_state* state = (struct lm_whisper_state*) opaque;
  return state ? state->start(state->context, index) * 10 : 0;
}

int64_t lm_whisper_segment_end(lm_whisper_handle opaque, int index) {
  struct lm_whisper_state* state = (struct lm_whisper_state*) opaque;
  return state ? state->end(state->context, index) * 10 : 0;
}

const char* lm_whisper_segment_text(lm_whisper_handle opaque, int index) {
  struct lm_whisper_state* state = (struct lm_whisper_state*) opaque;
  return state ? state->text(state->context, index) : "";
}

void lm_whisper_close(lm_whisper_handle opaque) {
  struct lm_whisper_state* state = (struct lm_whisper_state*) opaque;
  if (!state) return;
  if (state->context) state->free_context(state->context);
  if (state->library) LM_DLCLOSE(state->library);
  free(state->language);
  free(state);
}

const char* lm_whisper_last_error(void) { return last_error; }
