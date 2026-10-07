#if defined(__clang__) && defined(__x86_64__)
// clang ignores "#pragma GCC target" and leaves the intrinsics below without
// the declared target, so the AVX2 path does not compile at all. This is the
// clang equivalent; GCC and non-x86 targets keep the original branch.
#pragma clang attribute push(__attribute__((target("avx2,fma"))), apply_to = function)
#elif (defined(__GNUC__) || defined(__clang__)) && !defined(_MSC_VER)
#pragma GCC target("avx2,fma")
#endif

/*
 *  Copyright (c) 2020 The WebRTC project authors. All Rights Reserved.
 *
 *  Use of this source code is governed by a BSD-style license
 *  that can be found in the LICENSE file in the root of the source
 *  tree. An additional intellectual property rights grant can be found
 *  in the file PATENTS.  All contributing project authors may
 *  be found in the AUTHORS file in the root of the source tree.
 */

#include <immintrin.h>

#include <cstddef>
#include <span>

#include "audio_processing/aec3/aec3_common.h"
#include "audio_processing/aec3/fft_data.h"
#include "rtc_base/checks.h"

namespace webrtc {

// Computes the power spectrum of the data.
void FftData::SpectrumAVX2(std::span<float> power_spectrum) const {
  RTC_DCHECK_EQ(kFftLengthBy2Plus1, power_spectrum.size());
  for (size_t k = 0; k < kFftLengthBy2; k += 8) {
    __m256 r = _mm256_loadu_ps(&re[k]);
    __m256 i = _mm256_loadu_ps(&im[k]);
    __m256 ii = _mm256_mul_ps(i, i);
    ii = _mm256_fmadd_ps(r, r, ii);
    _mm256_storeu_ps(&power_spectrum[k], ii);
  }
  power_spectrum[kFftLengthBy2] = re[kFftLengthBy2] * re[kFftLengthBy2] +
                                  im[kFftLengthBy2] * im[kFftLengthBy2];
}

}  // namespace webrtc

#if defined(__clang__) && defined(__x86_64__)
#pragma clang attribute pop
#endif
