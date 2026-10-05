#ifndef RTC_BASE_EXPERIMENTS_FIELD_TRIAL_PARSER_H_
#define RTC_BASE_EXPERIMENTS_FIELD_TRIAL_PARSER_H_

#include <cstdlib>
#include <initializer_list>
#include <optional>
#include <string>

#include "absl/strings/string_view.h"

namespace webrtc {

class FieldTrialParameterInterface {
 public:
  virtual ~FieldTrialParameterInterface() = default;
  std::string key() const { return key_; }

 public:
  explicit FieldTrialParameterInterface(absl::string_view key)
      : key_(key.data(), key.size()) {}
  FieldTrialParameterInterface(const FieldTrialParameterInterface&) = default;
  FieldTrialParameterInterface& operator=(
      const FieldTrialParameterInterface&) = default;
  virtual bool Parse(std::optional<std::string> str_value) = 0;

 private:
  std::string key_;
};

void ParseFieldTrial(
    std::initializer_list<FieldTrialParameterInterface*> fields,
    absl::string_view trial_string);

template <typename T>
std::optional<T> ParseTypedParameter(absl::string_view);

template <typename T>
class FieldTrialParameter : public FieldTrialParameterInterface {
 public:
  FieldTrialParameter(absl::string_view key, T& value_to_update)
      : FieldTrialParameterInterface(key), value_ptr_(&value_to_update) {}
  T Get() const { return *value_ptr_; }
  operator T() const { return Get(); }
  const T* operator->() const { return value_ptr_; }

  bool Parse(std::optional<std::string> str_value) override {
    if (str_value) {
      std::optional<T> value = ParseTypedParameter<T>(*str_value);
      if (value.has_value()) {
        *value_ptr_ = value.value();
        return true;
      }
    }
    return false;
  }

 private:
  T* value_ptr_;
};

}  // namespace webrtc

#endif  // RTC_BASE_EXPERIMENTS_FIELD_TRIAL_PARSER_H_