#ifndef ABSL_STRINGS_MATCH_H_
#define ABSL_STRINGS_MATCH_H_

#include "absl/strings/string_view.h"

namespace absl {

inline bool StartsWith(absl::string_view text, absl::string_view prefix) {
  return text.size() >= prefix.size() &&
         text.compare(0, prefix.size(), prefix) == 0;
}

inline bool EndsWith(absl::string_view text, absl::string_view suffix) {
  return text.size() >= suffix.size() &&
         text.compare(text.size() - suffix.size(), suffix.size(), suffix) == 0;
}

}  // namespace absl

#endif  // ABSL_STRINGS_MATCH_H_