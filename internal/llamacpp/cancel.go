package llamacpp

/*
#include "bridge.h"
*/
import "C"

// setCancelled arms or clears the flag that the native generation loop polls
// between tokens. It is written from the goroutine that watches the request
// context and read by the native loop, so the flag lives in C with C11 atomic
// access instead of being a plain Go variable.
func setCancelled(value bool) {
	if value {
		C.lm_llama_set_cancelled(1)
		return
	}
	C.lm_llama_set_cancelled(0)
}
