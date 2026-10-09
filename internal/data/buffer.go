// Package data provides object bodies and object size distributions.
package data

import (
	"math/rand/v2"
	"sync"
)

// BufferSize is the size of the shared random buffer.
const BufferSize = 64 << 20

var (
	sharedOnce sync.Once
	shared     []byte
)

// SharedBuffer returns the process-wide random buffer.
// It is generated on the first call and must be treated as read-only.
func SharedBuffer() []byte {
	sharedOnce.Do(func() {
		shared = newRandomBuffer(BufferSize, rand.Uint64())
	})
	return shared
}

func newRandomBuffer(size int, seed uint64) []byte {
	var key [32]byte
	for i := range 4 {
		v := seed + uint64(i)
		for j := range 8 {
			key[i*8+j] = byte(v >> (8 * j))
		}
	}
	buf := make([]byte, size)
	_, _ = rand.NewChaCha8(key).Read(buf)
	return buf
}
