#include "rtc_base/experiments/field_trial_parser.h"

#include <string>

namespace webrtc {

void ParseFieldTrial(
    std::initializer_list<FieldTrialParameterInterface*> fields,
    absl::string_view trial_string) {
  if (trial_string.empty()) {
    return;
  }
  // Simple key=value parser. Format: key1:value1,key2:value2
  // For our purposes (NoOpFieldTrials), this is sufficient.
  std::string trial_str(trial_string.data(), trial_string.size());
  // Parse comma-separated key:value pairs.
  size_t pos = 0;
  while (pos < trial_str.size()) {
    // Find next comma
    size_t comma = trial_str.find(',', pos);
    std::string pair = trial_str.substr(pos, comma - pos);

    // Find colon separator
    size_t colon = pair.find(':');
    std::string key;
    std::optional<std::string> value;
    if (colon != std::string::npos) {
      key = pair.substr(0, colon);
      value = pair.substr(colon + 1);
    } else {
      key = pair;
      value = std::nullopt;
    }

    // Try to match the key with a field
    for (auto* field : fields) {
      if (field->key() == key) {
        field->Parse(value);
        break;
      }
    }

    if (comma == std::string::npos) {
      break;
    }
    pos = comma + 1;
  }
}

template <>
std::optional<int> ParseTypedParameter<int>(absl::string_view str) {
  std::string s(str.data(), str.size());
  try {
    return std::stoi(s);
  } catch (...) {
    return std::nullopt;
  }
}

template <>
std::optional<double> ParseTypedParameter<double>(absl::string_view str) {
  std::string s(str.data(), str.size());
  try {
    return std::stod(s);
  } catch (...) {
    return std::nullopt;
  }
}

template <>
std::optional<float> ParseTypedParameter<float>(absl::string_view str) {
  std::string s(str.data(), str.size());
  try {
    return std::stof(s);
  } catch (...) {
    return std::nullopt;
  }
}

template <>
std::optional<bool> ParseTypedParameter<bool>(absl::string_view str) {
  if (str == "true" || str == "1" || str == "Enabled") {
    return true;
  }
  if (str == "false" || str == "0" || str == "Disabled") {
    return false;
  }
  return std::nullopt;
}

}  // namespace webrtc