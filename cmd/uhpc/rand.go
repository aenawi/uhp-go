package main

import (
	"crypto/rand"
	"encoding/hex"
)

// randomHex returns n random bytes as hex.
//
// It panics rather than degrading to a weaker source. The only caller mints
// idempotency keys, and a key that is not unique is worse than no key at all:
// two tasks colliding onto one means the second is answered with the first
// one's result, silently and with no way for either caller to notice.
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("uhpc: no source of randomness for an idempotency key: " + err.Error())
	}
	return hex.EncodeToString(b)
}
