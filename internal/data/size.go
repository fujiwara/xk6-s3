package data

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
)

// MaxObjectSize is the upper bound of a sampled object size (5TiB, the S3 limit).
const MaxObjectSize int64 = 5 << 40

var units = map[string]int64{
	"":    1,
	"b":   1,
	"kb":  1000,
	"mb":  1000 * 1000,
	"gb":  1000 * 1000 * 1000,
	"tb":  1000 * 1000 * 1000 * 1000,
	"kib": 1 << 10,
	"mib": 1 << 20,
	"gib": 1 << 30,
	"tib": 1 << 40,
}

// ParseBytes parses a byte size such as "1048576", "512KiB", "1.5MiB" or "10MB".
// Binary units (KiB, MiB, ...) are powers of 1024 and SI units (KB, MB, ...)
// are powers of 1000. Units are case-insensitive.
func ParseBytes(s string) (int64, error) {
	t := strings.TrimSpace(s)
	i := strings.IndexFunc(t, func(r rune) bool {
		return (r < '0' || r > '9') && r != '.'
	})
	if i < 0 {
		i = len(t)
	}
	num, unit := t[:i], strings.ToLower(strings.TrimSpace(t[i:]))
	mul, ok := units[unit]
	if !ok {
		return 0, fmt.Errorf("invalid size %q: unknown unit %q", s, t[i:])
	}
	if n, err := strconv.ParseInt(num, 10, 64); err == nil {
		if n > math.MaxInt64/mul {
			return 0, fmt.Errorf("invalid size %q: too large", s)
		}
		return n * mul, nil
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	v := f * float64(mul)
	if v >= math.MaxInt64 {
		return 0, fmt.Errorf("invalid size %q: too large", s)
	}
	return int64(math.Round(v)), nil
}

// Sizer samples object sizes.
type Sizer interface {
	Next(r *rand.Rand) int64
}

// Fixed always returns the same size.
type Fixed int64

// Next implements Sizer.
func (f Fixed) Next(*rand.Rand) int64 { return int64(f) }

// Uniform samples sizes uniformly from [Min, Max].
type Uniform struct {
	Min, Max int64
}

// Next implements Sizer.
func (u Uniform) Next(r *rand.Rand) int64 {
	return u.Min + r.Int64N(u.Max-u.Min+1)
}

// LogNormal samples sizes from a log-normal distribution with the given
// median and sigma (the standard deviation of the underlying normal
// distribution). Samples are clamped to [0, MaxObjectSize].
type LogNormal struct {
	Median int64
	Sigma  float64
}

// Next implements Sizer.
func (l LogNormal) Next(r *rand.Rand) int64 {
	v := math.Exp(math.Log(float64(l.Median)) + l.Sigma*r.NormFloat64())
	if v >= float64(MaxObjectSize) {
		return MaxObjectSize
	}
	return int64(math.Round(v))
}

// Choice samples one of Sizes with probability proportional to its weight.
type Choice struct {
	Sizes []int64
	// cumulative weights
	cum []float64
}

// NewChoice returns a Choice. sizes and weights must have the same non-zero
// length and every weight must be positive.
func NewChoice(sizes []int64, weights []float64) (*Choice, error) {
	if len(sizes) == 0 || len(sizes) != len(weights) {
		return nil, errors.New("choice: sizes and weights must have the same non-zero length")
	}
	cum := make([]float64, len(weights))
	var total float64
	for i, w := range weights {
		if !(w > 0) || math.IsInf(w, 0) {
			return nil, fmt.Errorf("choice: weight must be a positive number: %v", w)
		}
		total += w
		cum[i] = total
	}
	return &Choice{Sizes: slices.Clone(sizes), cum: cum}, nil
}

// Next implements Sizer.
func (c *Choice) Next(r *rand.Rand) int64 {
	x := r.Float64() * c.cum[len(c.cum)-1]
	i, _ := slices.BinarySearchFunc(c.cum, x, func(e, t float64) int {
		if e <= t {
			return -1
		}
		return 1
	})
	return c.Sizes[min(i, len(c.Sizes)-1)]
}

// ParseSizer parses a size specification exported from JavaScript:
//
//   - a number (bytes) or a string with a unit, e.g. "1MiB"
//   - {dist: "uniform", min, max}
//   - {dist: "lognormal", median, sigma}
//   - {dist: "choice", values: [{size, weight}, ...]}
func ParseSizer(v any) (Sizer, error) {
	if m, ok := v.(map[string]any); ok {
		return parseDist(m)
	}
	n, err := toBytes(v)
	if err != nil {
		return nil, err
	}
	return Fixed(n), nil
}

func parseDist(m map[string]any) (Sizer, error) {
	switch dist := m["dist"]; dist {
	case "uniform":
		lo, err := field(m, "min", toBytes)
		if err != nil {
			return nil, err
		}
		hi, err := field(m, "max", toBytes)
		if err != nil {
			return nil, err
		}
		if lo > hi {
			return nil, fmt.Errorf("uniform: min %d is greater than max %d", lo, hi)
		}
		return Uniform{Min: lo, Max: hi}, nil
	case "lognormal":
		median, err := field(m, "median", toBytes)
		if err != nil {
			return nil, err
		}
		if median <= 0 {
			return nil, fmt.Errorf("lognormal: median must be positive: %d", median)
		}
		sigma, err := field(m, "sigma", toFloat)
		if err != nil {
			return nil, err
		}
		if sigma < 0 {
			return nil, fmt.Errorf("lognormal: sigma must not be negative: %v", sigma)
		}
		return LogNormal{Median: median, Sigma: sigma}, nil
	case "choice":
		values, ok := m["values"].([]any)
		if !ok {
			return nil, errors.New("choice: values must be an array")
		}
		sizes := make([]int64, len(values))
		weights := make([]float64, len(values))
		for i, e := range values {
			em, ok := e.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("choice: values[%d] must be an object", i)
			}
			s, err := field(em, "size", toBytes)
			if err != nil {
				return nil, fmt.Errorf("choice: values[%d]: %w", i, err)
			}
			w, err := field(em, "weight", toFloat)
			if err != nil {
				return nil, fmt.Errorf("choice: values[%d]: %w", i, err)
			}
			sizes[i], weights[i] = s, w
		}
		return NewChoice(sizes, weights)
	case nil:
		return nil, errors.New("size distribution requires dist")
	default:
		return nil, fmt.Errorf("unknown size distribution: %v", dist)
	}
}

func field[T any](m map[string]any, name string, conv func(any) (T, error)) (T, error) {
	v, ok := m[name]
	if !ok {
		var zero T
		return zero, fmt.Errorf("%s is required", name)
	}
	t, err := conv(v)
	if err != nil {
		return t, fmt.Errorf("%s: %w", name, err)
	}
	return t, nil
}

func toBytes(v any) (int64, error) {
	switch n := v.(type) {
	case int64:
		if n < 0 {
			return 0, fmt.Errorf("negative size: %d", n)
		}
		return n, nil
	case int:
		return toBytes(int64(n))
	case float64:
		if n < 0 || math.IsNaN(n) || n >= math.MaxInt64 || n != math.Trunc(n) {
			return 0, fmt.Errorf("size must be a non-negative integer: %v", n)
		}
		return int64(n), nil
	case string:
		return ParseBytes(n)
	default:
		return 0, fmt.Errorf("invalid size: %v (%T)", v, v)
	}
}

func toFloat(v any) (float64, error) {
	switch n := v.(type) {
	case int64:
		return float64(n), nil
	case int:
		return float64(n), nil
	case float64:
		if math.IsNaN(n) {
			return 0, errors.New("NaN is not allowed")
		}
		return n, nil
	default:
		return 0, fmt.Errorf("not a number: %v (%T)", v, v)
	}
}

// SizeClass returns the size_class tag value for an object size.
func SizeClass(size int64) string {
	switch {
	case size < 4<<10:
		return "<4KiB"
	case size < 64<<10:
		return "<64KiB"
	case size < 1<<20:
		return "<1MiB"
	case size < 16<<20:
		return "<16MiB"
	case size < 128<<20:
		return "<128MiB"
	default:
		return ">=128MiB"
	}
}
