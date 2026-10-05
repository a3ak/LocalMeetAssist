#ifndef ABSL_META_TYPE_TRAITS_H_
#define ABSL_META_TYPE_TRAITS_H_

#include <type_traits>

namespace absl {

template <bool B, typename T = void>
using enable_if_t = typename std::enable_if<B, T>::type;

template <typename T>
using underlying_type_t = typename std::underlying_type<T>::type;

template <typename T>
using decay_t = typename std::decay<T>::type;

template <typename T>
using is_trivially_copy_constructible = std::is_trivially_copy_constructible<T>;

template <typename T>
using is_trivially_copy_assignable = std::is_trivially_copy_assignable<T>;

template <typename T>
using is_trivially_destructible = std::is_trivially_destructible<T>;

}  // namespace absl

#endif  // ABSL_META_TYPE_TRAITS_H_