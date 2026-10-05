#ifndef ABSL_BASE_NULLABILITY_H_
#define ABSL_BASE_NULLABILITY_H_

// Stub: absl_nonnull is a Clang nullability annotation.
// It is disabled everywhere: some Clang versions reject _Nonnull applied to
// non-pointer types (e.g. std::unique_ptr used as a return type in
// echo_control.h), and it carries no runtime semantics anyway.
#ifndef absl_nonnull
#define absl_nonnull
#endif

#endif  // ABSL_BASE_NULLABILITY_H_
