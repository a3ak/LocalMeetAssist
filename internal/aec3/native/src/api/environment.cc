/*
 *  Copyright 2023 The WebRTC Project Authors. All rights reserved.
 *
 *  Use of this source code is governed by a BSD-style license
 *  that can be found in the LICENSE file in the root of the source
 *  tree. An additional intellectual property rights grant can be found
 *  in the file PATENTS.  All contributing project authors may
 *  be found in the AUTHORS file in the root of the source tree.
 */

#include "api/environment.h"

#include <memory>

namespace webrtc {

namespace {

// Concrete Clock implementation that returns the real-time clock.
class RealTimeClock : public Clock {
 public:
  static RealTimeClock* Instance() {
    static RealTimeClock instance;
    return &instance;
  }
};

class StubTaskQueueFactory : public TaskQueueFactory {};
class StubRtcEventLog : public RtcEventLog {};

}  // namespace

Clock* Clock::GetRealTimeClock() {
  return RealTimeClock::Instance();
}

Environment::Environment()
    : field_trials_(std::make_shared<NoOpFieldTrials>()),
      clock_(std::shared_ptr<Clock>(RealTimeClock::Instance(),
                                    [](Clock*) {})),
      task_queue_factory_(std::make_shared<StubTaskQueueFactory>()),
      event_log_(std::make_shared<StubRtcEventLog>()) {}

Clock& Environment::clock() const {
  if (!clock_) {
    clock_ = std::shared_ptr<Clock>(RealTimeClock::Instance(),
                                    [](Clock*) {});
  }
  return *clock_;
}

TaskQueueFactory& Environment::task_queue_factory() const {
  if (!task_queue_factory_) {
    task_queue_factory_ = std::make_shared<StubTaskQueueFactory>();
  }
  return *task_queue_factory_;
}

RtcEventLog& Environment::event_log() const {
  if (!event_log_) {
    event_log_ = std::make_shared<StubRtcEventLog>();
  }
  return *event_log_;
}

}  // namespace webrtc