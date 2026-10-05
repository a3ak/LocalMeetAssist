/*
 *  Copyright 2023 The WebRTC Project Authors. All rights reserved.
 *
 *  Use of this source code is governed by a BSD-style license
 *  that can be found in the LICENSE file in the root of the source
 *  tree. An additional intellectual property rights grant can be found
 *  in the file PATENTS.  All contributing project authors may
 *  be found in the AUTHORS file in the root of the source tree.
 */

#ifndef RTC_BASE_CPU_INFO_H_
#define RTC_BASE_CPU_INFO_H_

namespace cpu_info {

enum class ISA {
  kSSE2,
  kAVX2,
};

inline bool Supports(ISA) {
  return false;
}

}  // namespace cpu_info

#endif  // RTC_BASE_CPU_INFO_H_