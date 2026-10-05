#include "bridge.h"

#include <algorithm>
#include <exception>
#include <memory>
#include <string>
#include <vector>

#include "api/echo_canceller3_config.h"
#include "api/echo_canceller3_factory.h"
#include "api/echo_control.h"
#include "api/environment.h"
#include "audio_processing/audio_buffer.h"

namespace {
thread_local std::string last_error;

struct State {
  int sample_rate;
  int frame_samples;
  std::unique_ptr<webrtc::EchoControl> echo;
  webrtc::AudioBuffer render;
  webrtc::AudioBuffer capture;

  State(int rate, int delay_ms)
      : sample_rate(rate),
        frame_samples(rate / 100),
        render(rate, 1, rate, 1, rate, 1),
        capture(rate, 1, rate, 1, rate, 1) {
    webrtc::EchoCanceller3Config config;
    config.filter.initial_state_seconds = 0.5f;
    config.filter.conservative_initial_phase = false;
    webrtc::Environment environment;
    webrtc::EchoCanceller3Factory factory(config);
    echo = factory.Create(environment, rate, 1, 1);
    echo->SetAudioBufferDelay(std::max(0, delay_ms));
  }
};

void fill(webrtc::AudioBuffer& buffer, const int16_t* source, int count) {
  float* channel = buffer.channels()[0];
  for (int i = 0; i < count; ++i) channel[i] = source[i];
}
}  // namespace

extern "C" lm_aec3_handle lm_aec3_create(int sample_rate, int delay_ms) {
  try {
    last_error.clear();
    if (sample_rate != 16000 && sample_rate != 32000 && sample_rate != 48000) {
      last_error = "AEC3 supports 16000, 32000 or 48000 Hz";
      return nullptr;
    }
    return new State(sample_rate, delay_ms);
  } catch (const std::exception& error) {
    last_error = error.what();
    return nullptr;
  } catch (...) {
    last_error = "unknown AEC3 initialization error";
    return nullptr;
  }
}

extern "C" int lm_aec3_process(lm_aec3_handle opaque, const int16_t* render,
                                const int16_t* capture, int16_t* output,
                                int samples) {
  try {
    last_error.clear();
    auto* state = static_cast<State*>(opaque);
    if (!state || !render || !capture || !output || samples != state->frame_samples) {
      last_error = "invalid AEC3 frame";
      return -1;
    }
    fill(state->render, render, samples);
    state->echo->AnalyzeRender(&state->render);
    fill(state->capture, capture, samples);
    state->echo->AnalyzeCapture(&state->capture);
    state->echo->ProcessCapture(&state->capture, false);
    const float* channel = state->capture.channels_const()[0];
    for (int i = 0; i < samples; ++i) {
      output[i] = static_cast<int16_t>(std::clamp(channel[i], -32768.0f, 32767.0f));
    }
    return 0;
  } catch (const std::exception& error) {
    last_error = error.what();
    return -1;
  } catch (...) {
    last_error = "unknown AEC3 processing error";
    return -1;
  }
}

extern "C" void lm_aec3_destroy(lm_aec3_handle opaque) {
  delete static_cast<State*>(opaque);
}

extern "C" const char* lm_aec3_last_error(void) { return last_error.c_str(); }
