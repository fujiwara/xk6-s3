package data

import (
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

func TestParseBytes(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"0", 0},
		{"1024", 1024},
		{"100B", 100},
		{"4KiB", 4 << 10},
		{"512kib", 512 << 10},
		{"1MiB", 1 << 20},
		{"1.5MiB", 3 << 19},
		{"2GiB", 2 << 30},
		{"1TiB", 1 << 40},
		{" 8 MiB ", 8 << 20},
	}
	for _, tt := range tests {
		got, err := ParseBytes(tt.in)
		if err != nil {
			t.Errorf("ParseBytes(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseBytes(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}

	for _, in := range []string{"", "MiB", "-1", "1XB", "1.2.3KiB", "10000000TiB", "1e3"} {
		if got, err := ParseBytes(in); err == nil {
			t.Errorf("ParseBytes(%q) = %d, want error", in, got)
		}
	}
}

func TestParseBytesAmbiguousUnits(t *testing.T) {
	tests := []struct{ in, want string }{
		{"10KB", "use KiB"},
		{"10MB", "use MiB"},
		{"1gb", "use GiB"},
		{"1TB", "use TiB"},
		{"10M", "use MiB"},
		{"1k", "use KiB"},
	}
	for _, tt := range tests {
		_, err := ParseBytes(tt.in)
		if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "ambiguous") {
			t.Errorf("ParseBytes(%q) error = %v, want %q", tt.in, err, tt.want)
		}
	}
}

func TestParseSizerFixed(t *testing.T) {
	tests := []struct {
		in   any
		want int64
	}{
		{int64(4096), 4096},
		{float64(100), 100},
		{"1MiB", 1 << 20},
	}
	for _, tt := range tests {
		s, err := ParseSizer(tt.in)
		if err != nil {
			t.Errorf("ParseSizer(%v): %v", tt.in, err)
			continue
		}
		if got := s.Next(nil); got != tt.want {
			t.Errorf("ParseSizer(%v).Next() = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestParseSizerErrors(t *testing.T) {
	tests := []any{
		int64(-1),
		1.5,
		math.NaN(),
		true,
		nil,
		"abc",
		map[string]any{},
		map[string]any{"dist": "zipf"},
		map[string]any{"dist": "uniform", "min": "1KiB"},
		map[string]any{"dist": "uniform", "min": "2KiB", "max": "1KiB"},
		map[string]any{"dist": "lognormal", "median": int64(0), "sigma": 1.0},
		map[string]any{"dist": "lognormal", "median": "1MiB", "sigma": -1.0},
		map[string]any{"dist": "lognormal", "median": "1MiB"},
		map[string]any{"dist": "choice"},
		map[string]any{"dist": "choice", "values": []any{}},
		map[string]any{"dist": "choice", "values": []any{int64(1)}},
		map[string]any{"dist": "choice", "values": []any{map[string]any{"size": "1KiB", "weight": int64(0)}}},
		map[string]any{"dist": "choice", "values": []any{map[string]any{"size": "1KiB"}}},
	}
	for _, in := range tests {
		if _, err := ParseSizer(in); err == nil {
			t.Errorf("ParseSizer(%#v) succeeded, want error", in)
		}
	}
}

func newRand() *rand.Rand {
	return rand.New(rand.NewPCG(1, 2))
}

func TestUniform(t *testing.T) {
	s, err := ParseSizer(map[string]any{"dist": "uniform", "min": "1KiB", "max": int64(1034)})
	if err != nil {
		t.Fatal(err)
	}
	r := newRand()
	seen := map[int64]bool{}
	for range 10000 {
		v := s.Next(r)
		if v < 1024 || v > 1034 {
			t.Fatalf("out of range: %d", v)
		}
		seen[v] = true
	}
	if len(seen) != 11 {
		t.Errorf("saw %d distinct values, want 11 (both ends inclusive)", len(seen))
	}
}

func TestLogNormal(t *testing.T) {
	s, err := ParseSizer(map[string]any{"dist": "lognormal", "median": "1MiB", "sigma": 0.5})
	if err != nil {
		t.Fatal(err)
	}
	r := newRand()
	const n = 20001
	vs := make([]int64, n)
	for i := range vs {
		vs[i] = s.Next(r)
		if vs[i] < 0 {
			t.Fatalf("negative size: %d", vs[i])
		}
	}
	slices.Sort(vs)
	median := float64(vs[n/2])
	if math.Abs(median-(1<<20))/(1<<20) > 0.03 {
		t.Errorf("sample median = %v, want about %d", median, 1<<20)
	}

	zero := LogNormal{Median: 1000, Sigma: 0}
	if got := zero.Next(r); got != 1000 {
		t.Errorf("sigma 0: Next() = %d, want 1000", got)
	}

	huge := LogNormal{Median: 1 << 40, Sigma: 100}
	for range 100 {
		if v := huge.Next(r); v < 0 || v > MaxObjectSize {
			t.Fatalf("not clamped: %d", v)
		}
	}
}

func TestChoice(t *testing.T) {
	s, err := ParseSizer(map[string]any{
		"dist": "choice",
		"values": []any{
			map[string]any{"size": "4KiB", "weight": int64(1)},
			map[string]any{"size": "1MiB", "weight": 3.0},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := newRand()
	const n = 40000
	counts := map[int64]int{}
	for range n {
		counts[s.Next(r)]++
	}
	if len(counts) != 2 {
		t.Fatalf("unexpected values: %v", counts)
	}
	if ratio := float64(counts[1<<20]) / n; math.Abs(ratio-0.75) > 0.02 {
		t.Errorf("1MiB ratio = %v, want about 0.75", ratio)
	}
}

func TestSizeClass(t *testing.T) {
	tests := []struct {
		size int64
		want string
	}{
		{0, "<4KiB"},
		{4<<10 - 1, "<4KiB"},
		{4 << 10, "<64KiB"},
		{64 << 10, "<1MiB"},
		{1 << 20, "<16MiB"},
		{16 << 20, "<128MiB"},
		{128<<20 - 1, "<128MiB"},
		{128 << 20, ">=128MiB"},
		{MaxObjectSize, ">=128MiB"},
	}
	for _, tt := range tests {
		if got := SizeClass(tt.size); got != tt.want {
			t.Errorf("SizeClass(%d) = %q, want %q", tt.size, got, tt.want)
		}
	}
}
