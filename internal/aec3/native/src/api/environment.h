/*
 *  Copyright 2023 The WebRTC Project Authors. All rights reserved.
 *
 *  Use of this source code is governed by a BSD-style license
 *  that can be found in the LICENSE file in the root of the source
 *  tree. An additional intellectual property rights grant can be found
 *  in the file PATENTS.  All contributing project authors may
 *  be found in the AUTHORS file in the root of the source tree.
 */

#ifndef API_ENVIRONMENT_H_
#define API_ENVIRONMENT_H_

#include <memory>
#include <string>

#include "absl/strings/string_view.h"
#include "api/field_trials_view.h"
#include "rtc_base/system/rtc_export.h"

namespace webrtc {

// A concrete FieldTrialsView implementation that returns empty strings for
// all lookups (i.e., no field trials are enabled).
class RTC_EXPORT NoOpFieldTrials : public FieldTrialsView {
 public:
  std::string Lookup(absl::string_view key) const override { return ""; }
  std::unique_ptr<FieldTrialsView> CreateCopy() const override {
    return std::make_unique<NoOpFieldTrials>();
  }
};

// Minimal Clock stub for AEC3 standalone usage.
class RTC_EXPORT Clock {
 public:
  virtual ~Clock() = default;
  static Clock* GetRealTimeClock();
};

// Minimal TaskQueueFactory stub.
class RTC_EXPORT TaskQueueFactory {
 public:
  virtual ~TaskQueueFactory() = default;
};

// Minimal RtcEventLog stub.
class RTC_EXPORT RtcEventLog {
 public:
  virtual ~RtcEventLog() = default;
};

// Simplified Environment stub for AEC3 standalone usage.
// Uses shared_ptr for shared ownership, allowing copy semantics.
class RTC_EXPORT Environment {
 public:
  Environment();
  Environment(const Environment&) = default;
  Environment(Environment&&) = default;
  Environment& operator=(const Environment&) = default;
  Environment& operator=(Environment&&) = default;
  ~Environment() = default;

  const FieldTrialsView& field_trials() const {
    return *field_trials_;
  }

  Clock& clock() const;
  TaskQueueFactory& task_queue_factory() const;
  RtcEventLog& event_log() const;

 private:
  std::shared_ptr<FieldTrialsView> field_trials_;
  mutable std::shared_ptr<Clock> clock_;
  mutable std::shared_ptr<TaskQueueFactory> task_queue_factory_;
  mutable std::shared_ptr<RtcEventLog> event_log_;
};

}  // namespace webrtc

#endif  // API_ENVIRONMENT_H_