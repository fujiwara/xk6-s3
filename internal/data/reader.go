package data

import (
	"errors"
	"fmt"
	"io"
)

// Reader reads size bytes from buf starting at offset, wrapping around to the
// beginning of buf when it reaches the end. It implements io.ReadSeeker so
// that the SDK can rewind the body for signing and checksums.
type Reader struct {
	buf    []byte
	offset int64
	size   int64
	pos    int64
}

var _ io.ReadSeeker = (*Reader)(nil)

// NewReader returns a Reader of size bytes starting at offset in buf.
// offset is reduced modulo len(buf).
func NewReader(buf []byte, offset, size int64) (*Reader, error) {
	if len(buf) == 0 {
		return nil, errors.New("empty buffer")
	}
	if size < 0 {
		return nil, fmt.Errorf("negative size: %d", size)
	}
	offset %= int64(len(buf))
	if offset < 0 {
		offset += int64(len(buf))
	}
	return &Reader{buf: buf, offset: offset, size: size}, nil
}

// Offset returns the start offset in the buffer.
func (r *Reader) Offset() int64 {
	return r.offset
}

// Size returns the total number of bytes the Reader produces.
func (r *Reader) Size() int64 {
	return r.size
}

// Len returns the number of unread bytes.
func (r *Reader) Len() int64 {
	if r.pos >= r.size {
		return 0
	}
	return r.size - r.pos
}

// Read implements io.Reader.
func (r *Reader) Read(p []byte) (int, error) {
	if r.pos >= r.size {
		return 0, io.EOF
	}
	if remain := r.size - r.pos; int64(len(p)) > remain {
		p = p[:remain]
	}
	n := 0
	bufLen := int64(len(r.buf))
	for n < len(p) {
		start := (r.offset + r.pos) % bufLen
		c := copy(p[n:], r.buf[start:])
		n += c
		r.pos += int64(c)
	}
	return n, nil
}

// Seek implements io.Seeker.
func (r *Reader) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = r.pos + offset
	case io.SeekEnd:
		abs = r.size + offset
	default:
		return 0, fmt.Errorf("invalid whence: %d", whence)
	}
	if abs < 0 {
		return 0, fmt.Errorf("negative position: %d", abs)
	}
	r.pos = abs
	return abs, nil
}
