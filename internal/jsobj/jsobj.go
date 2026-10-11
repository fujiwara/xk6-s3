// Package jsobj decodes JavaScript objects exported by sobek into Go structs.
//
// The struct tags define both the decoding and the generated API documents
// (internal/cmd/apidoc), so that the documents follow the code:
//
//   - js: the key of the field. Fields without it are ignored.
//   - default: the value of an absent key, written as in JavaScript without
//     quotes, such as 1, true, 5MiB or 60s.
//   - env: comma-separated environment variables for an absent key of a
//     string field. The first non-empty one is used before the default.
//   - enum: comma-separated allowed values of a string or []string field.
//   - min / max: the bounds of a number field. Integers are non-negative
//     unless min is set.
//   - ts: the TypeScript type in the documents, for a field whose Go type
//     does not tell it.
//
// A null key is absent, and so is an empty string for a field with env or
// default. A nested struct is decoded from a nested object, with its
// defaults when absent. Unknown keys are rejected.
package jsobj

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Decoder is implemented by field types that decode themselves.
type Decoder interface {
	DecodeJS(v any) error
}

var (
	decoderType  = reflect.TypeFor[Decoder]()
	durationType = reflect.TypeFor[time.Duration]()
)

// Decode decodes m into the struct that dst points to. lookupEnv is used
// for the env tags, and may be nil if there are none.
func Decode(m map[string]any, dst any, lookupEnv func(string) (string, bool)) error {
	v := reflect.ValueOf(dst)
	if v.Kind() != reflect.Pointer || v.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("jsobj: dst must be a pointer to a struct: %T", dst)
	}
	return decodeStruct("", m, v.Elem(), lookupEnv)
}

// Keys returns the js keys of a struct type in the field order.
func Keys(t reflect.Type) []string {
	var keys []string
	for f := range fields(t) {
		keys = append(keys, f.Tag.Get("js"))
	}
	return keys
}

func fields(t reflect.Type) func(func(reflect.StructField) bool) {
	return func(yield func(reflect.StructField) bool) {
		for f := range t.Fields() {
			if f.Tag.Get("js") != "" && !yield(f) {
				return
			}
		}
	}
}

func decodeStruct(path string, m map[string]any, v reflect.Value, lookupEnv func(string) (string, bool)) error {
	var errs []error
	keys := Keys(v.Type())
	var unknown []string
	for k := range m {
		if !slices.Contains(keys, k) {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		slices.Sort(unknown)
		where := ""
		if path != "" {
			where = " in " + strings.TrimSuffix(path, ".")
		}
		errs = append(errs, fmt.Errorf("unknown keys%s: %v (available: %v)", where, unknown, keys))
	}
	for f := range fields(v.Type()) {
		name := path + f.Tag.Get("js")
		raw, err := value(f, m[f.Tag.Get("js")], lookupEnv)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		if raw == nil && f.Type.Kind() == reflect.Struct && !isDecoder(f.Type) {
			raw = map[string]any{}
		}
		if raw != nil {
			if err := decodeValue(name, raw, f, v.FieldByIndex(f.Index), lookupEnv); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// value returns the value of a field: raw if present, or the value from
// the env or default tag. It returns nil if there is none.
func value(f reflect.StructField, raw any, lookupEnv func(string) (string, bool)) (any, error) {
	env, hasEnv := f.Tag.Lookup("env")
	def, hasDefault := f.Tag.Lookup("default")
	if s, ok := raw.(string); ok && s == "" && (hasEnv || hasDefault) {
		raw = nil
	}
	if raw != nil {
		return raw, nil
	}
	if hasEnv && lookupEnv != nil {
		for name := range strings.SplitSeq(env, ",") {
			if v, ok := lookupEnv(name); ok && v != "" {
				return v, nil
			}
		}
	}
	if !hasDefault {
		return nil, nil
	}
	if isDecoder(f.Type) || f.Type == durationType {
		return def, nil
	}
	switch f.Type.Kind() {
	case reflect.Bool:
		b, err := strconv.ParseBool(def)
		if err != nil {
			return nil, fmt.Errorf("invalid default %q", def)
		}
		return b, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(def, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid default %q", def)
		}
		return n, nil
	case reflect.Float32, reflect.Float64:
		n, err := strconv.ParseFloat(def, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid default %q", def)
		}
		return n, nil
	}
	return def, nil
}

func isDecoder(t reflect.Type) bool {
	return reflect.PointerTo(t).Implements(decoderType)
}

func decodeValue(name string, raw any, f reflect.StructField, v reflect.Value, lookupEnv func(string) (string, bool)) error {
	var enum []string
	if e, ok := f.Tag.Lookup("enum"); ok {
		enum = strings.Split(e, ",")
	}
	switch {
	case isDecoder(f.Type):
		if err := v.Addr().Interface().(Decoder).DecodeJS(raw); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	case f.Type == durationType:
		d, err := parseDuration(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		v.SetInt(int64(d))
		return nil
	default:
		switch f.Type.Kind() {
		case reflect.String:
			s, ok := raw.(string)
			if !ok {
				return fmt.Errorf("%s must be a string: %v", name, raw)
			}
			if enum != nil && !slices.Contains(enum, s) {
				return fmt.Errorf("%s must be one of %v: %q", name, enum, s)
			}
			v.SetString(s)
		case reflect.Bool:
			b, ok := raw.(bool)
			if !ok {
				return fmt.Errorf("%s must be a boolean: %v", name, raw)
			}
			v.SetBool(b)
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			n, ok := toFloat(raw)
			if !ok || n != math.Trunc(n) || n < math.MinInt64 || n >= math.MaxInt64 || v.OverflowInt(int64(n)) {
				return fmt.Errorf("%s must be an integer: %v", name, raw)
			}
			v.SetInt(int64(n))
		case reflect.Float32, reflect.Float64:
			n, ok := toFloat(raw)
			if !ok || math.IsNaN(n) {
				return fmt.Errorf("%s must be a number: %v", name, raw)
			}
			v.SetFloat(n)
		case reflect.Slice:
			if f.Type.Elem().Kind() != reflect.String {
				return fmt.Errorf("%s: unsupported type %s", name, f.Type)
			}
			list, ok := raw.([]any)
			if !ok {
				return fmt.Errorf("%s must be an array of strings: %v", name, raw)
			}
			s := reflect.MakeSlice(f.Type, len(list), len(list))
			for i, e := range list {
				str, ok := e.(string)
				if !ok {
					return fmt.Errorf("%s must be an array of strings: %v", name, raw)
				}
				if enum != nil && !slices.Contains(enum, str) {
					return fmt.Errorf("%s must contain only %v: %q", name, enum, str)
				}
				s.Index(i).SetString(str)
			}
			v.Set(s)
		case reflect.Struct:
			m, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("%s must be an object: %v", name, raw)
			}
			return decodeStruct(name+".", m, v, lookupEnv)
		default:
			return fmt.Errorf("%s: unsupported type %s", name, f.Type)
		}
	}
	return checkBounds(name, f, v)
}

// checkBounds checks the min and max tags of a number field.
func checkBounds(name string, f reflect.StructField, v reflect.Value) error {
	var n float64
	minTag, hasMin := f.Tag.Lookup("min")
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n = float64(v.Int())
		if !hasMin {
			minTag, hasMin = "0", true
		}
	case reflect.Float32, reflect.Float64:
		n = v.Float()
	default:
		return nil
	}
	if hasMin {
		lo, err := strconv.ParseFloat(minTag, 64)
		if err != nil {
			return fmt.Errorf("%s: invalid min %q", name, minTag)
		}
		if n < lo {
			return fmt.Errorf("%s must be at least %s: %v", name, minTag, n)
		}
	}
	if maxTag, ok := f.Tag.Lookup("max"); ok {
		hi, err := strconv.ParseFloat(maxTag, 64)
		if err != nil {
			return fmt.Errorf("%s: invalid max %q", name, maxTag)
		}
		if n > hi {
			return fmt.Errorf("%s must be at most %s: %v", name, maxTag, n)
		}
	}
	return nil
}

// parseDuration accepts a duration string such as "30s" or a number of
// milliseconds, following the k6 convention.
func parseDuration(v any) (time.Duration, error) {
	var d time.Duration
	switch t := v.(type) {
	case string:
		var err error
		if d, err = time.ParseDuration(t); err != nil {
			return 0, err
		}
	default:
		f, ok := toFloat(t)
		if !ok {
			return 0, fmt.Errorf("must be a duration string or milliseconds: %v", v)
		}
		d = time.Duration(f * float64(time.Millisecond))
	}
	if d <= 0 {
		return 0, fmt.Errorf("must be positive: %v", v)
	}
	return d, nil
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}
