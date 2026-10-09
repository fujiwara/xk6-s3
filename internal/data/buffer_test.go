package data

import (
	"bytes"
	"testing"
)

func TestSharedBuffer(t *testing.T) {
	a := SharedBuffer()
	if len(a) != BufferSize {
		t.Fatalf("len = %d, want %d", len(a), BufferSize)
	}
	if b := SharedBuffer(); &a[0] != &b[0] {
		t.Error("SharedBuffer returned a different buffer")
	}
}

func TestNewRandomBuffer(t *testing.T) {
	a := newRandomBuffer(1<<16, 1)
	if !bytes.Equal(a, newRandomBuffer(1<<16, 1)) {
		t.Error("same seed produced different buffers")
	}
	if bytes.Equal(a, newRandomBuffer(1<<16, 2)) {
		t.Error("different seeds produced the same buffer")
	}
	if bytes.Equal(a[:4096], make([]byte, 4096)) {
		t.Error("buffer is all zero")
	}
}
