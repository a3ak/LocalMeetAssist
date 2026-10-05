#ifndef ABSL_TYPES_OPTIONAL_H_
#define ABSL_TYPES_OPTIONAL_H_

#include <optional>

namespace absl {
template <typename T>
using optional = std::optional<T>;
using nullopt_t = std::nullopt_t;
using std::nullopt;
using std::in_place;
using std::in_place_t;
}  // namespace absl

#endif  // ABSL_TYPES_OPTIONAL_H_