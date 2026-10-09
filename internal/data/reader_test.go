package data

import (
	"bytes"
	"io"
	"testing"
)

// expected returns the bytes a Reader should produce, built naively.
func expected(buf []byte, offset, size int64) []byte {
	out := make([]byte, size)
	for i := range out {
		out[i] = buf[(offset+int64(i))%int64(len(buf))]
	}
	return out
}

func TestReader(t *testing.T) {
	buf := []byte("0123456789")
	tests := []struct {
		name         string
		offset, size int64
	}{
		{"empty", 3, 0},
		{"within buffer", 2, 5},
		{"to buffer end", 5, 5},
		{"wraps once", 7, 6},
		{"wraps many times", 3, 35},
		{"offset beyond buffer", 23, 4},
		{"negative offset", -1, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			off := tt.offset % int64(len(buf))
			if off < 0 {
				off += int64(len(buf))
			}
			want := expected(buf, off, tt.size)
			for _, chunk := range []int{1, 3, 64} {
				r, err := NewReader(buf, tt.offset, tt.size)
				if err != nil {
					t.Fatal(err)
				}
				got, err := io.ReadAll(&chunkReader{r, chunk})
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, want) {
					t.Errorf("chunk %d: got %q, want %q", chunk, got, want)
				}
				if r.Len() != 0 {
					t.Errorf("Len after EOF = %d", r.Len())
				}
			}
		})
	}
}

// chunkReader limits each Read to n bytes.
type chunkReader struct {
	r io.Reader
	n int
}

func (c *chunkReader) Read(p []byte) (int, error) {
	return c.r.Read(p[:min(len(p), c.n)])
}

func TestReaderSeek(t *testing.T) {
	buf := []byte("abcdefgh")
	r, err := NewReader(buf, 6, 12)
	if err != nil {
		t.Fatal(err)
	}
	all, _ := io.ReadAll(r)
	if string(all) != "ghabcdefghab" {
		t.Fatalf("ReadAll = %q", all)
	}

	tests := []struct {
		offset int64
		whence int
		pos    int64
		rest   string
	}{
		{0, io.SeekStart, 0, "ghabcdefghab"},
		{3, io.SeekStart, 3, "bcdefghab"},
		{2, io.SeekCurrent, 5, "defghab"},
		{-2, io.SeekEnd, 10, "ab"},
		{0, io.SeekEnd, 12, ""},
		{20, io.SeekStart, 20, ""},
	}
	pos := int64(0)
	for _, tt := range tests {
		if tt.whence == io.SeekCurrent {
			if _, err := r.Seek(pos, io.SeekStart); err != nil {
				t.Fatal(err)
			}
		}
		got, err := r.Seek(tt.offset, tt.whence)
		if err != nil {
			t.Fatalf("Seek(%d, %d): %v", tt.offset, tt.whence, err)
		}
		if got != tt.pos {
			t.Errorf("Seek(%d, %d) = %d, want %d", tt.offset, tt.whence, got, tt.pos)
		}
		rest, _ := io.ReadAll(r)
		if string(rest) != tt.rest {
			t.Errorf("after Seek(%d, %d): read %q, want %q", tt.offset, tt.whence, rest, tt.rest)
		}
		pos = 3
	}

	if _, err := r.Seek(-1, io.SeekStart); err == nil {
		t.Error("Seek to negative position succeeded")
	}
	if _, err := r.Seek(0, 99); err == nil {
		t.Error("Seek with invalid whence succeeded")
	}
}

func TestNewReaderErrors(t *testing.T) {
	if _, err := NewReader(nil, 0, 1); err == nil {
		t.Error("empty buffer accepted")
	}
	if _, err := NewReader([]byte("x"), 0, -1); err == nil {
		t.Error("negative size accepted")
	}
}

func TestReaderLargerThanSharedBuffer(t *testing.T) {
	buf := SharedBuffer()
	size := int64(BufferSize + BufferSize/2)
	r, err := NewReader(buf, BufferSize-100, size)
	if err != nil {
		t.Fatal(err)
	}
	n, err := io.Copy(io.Discard, r)
	if err != nil {
		t.Fatal(err)
	}
	if n != size {
		t.Errorf("read %d bytes, want %d", n, size)
	}
}
