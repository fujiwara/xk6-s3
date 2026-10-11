package jsobj

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type bytes int64

func (b *bytes) DecodeJS(v any) error {
	s, ok := v.(string)
	if !ok || !strings.HasSuffix(s, "B") {
		return errors.New("must end with B")
	}
	*b = bytes(len(s))
	return nil
}

type nested struct {
	Rate float64 `js:"rate" default:"0.5" min:"0" max:"1"`
	On   bool    `js:"on"`
}

type target struct {
	Name     string        `js:"name" env:"T_NAME,T_NAME2" default:"def"`
	Mode     string        `js:"mode" enum:"a,b" default:"a"`
	Flag     bool          `js:"flag" default:"true"`
	Count    int64         `js:"count" default:"3"`
	Attempts int           `js:"attempts" default:"1" min:"1"`
	Timeout  time.Duration `js:"timeout" default:"60s"`
	Size     bytes         `js:"size" default:"xxB" min:"1"`
	Tags     []string      `js:"tags" enum:"x,y"`
	Nested   nested        `js:"nested"`
	ignored  int
}

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestDecodeDefaults(t *testing.T) {
	var got target
	if err := Decode(map[string]any{}, &got, env(nil)); err != nil {
		t.Fatal(err)
	}
	want := target{
		Name: "def", Mode: "a", Flag: true, Count: 3, Attempts: 1, Timeout: time.Minute, Size: 3,
		Nested: nested{Rate: 0.5},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Decode() = %+v, want %+v", got, want)
	}
}

func TestDecodeValues(t *testing.T) {
	var got target
	err := Decode(map[string]any{
		"name":     "n",
		"mode":     "b",
		"flag":     false,
		"count":    float64(0),
		"attempts": int64(5),
		"timeout":  int64(1500),
		"size":     "abcdB",
		"tags":     []any{"y", "x"},
		"nested":   map[string]any{"on": true},
	}, &got, env(map[string]string{"T_NAME": "env"}))
	if err != nil {
		t.Fatal(err)
	}
	want := target{
		Name: "n", Mode: "b", Flag: false, Count: 0, Attempts: 5, Timeout: 1500 * time.Millisecond, Size: 5,
		Tags: []string{"y", "x"}, Nested: nested{Rate: 0.5, On: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Decode() = %+v, want %+v", got, want)
	}
}

func TestDecodeEnv(t *testing.T) {
	tests := []struct {
		m    map[string]any
		env  map[string]string
		want string
	}{
		{nil, map[string]string{"T_NAME": "1", "T_NAME2": "2"}, "1"},
		{nil, map[string]string{"T_NAME": "", "T_NAME2": "2"}, "2"},
		{map[string]any{"name": ""}, map[string]string{"T_NAME2": "2"}, "2"},
		{map[string]any{"name": nil}, nil, "def"},
		{map[string]any{"name": "x"}, map[string]string{"T_NAME": "1"}, "x"},
	}
	for _, tt := range tests {
		var got target
		if err := Decode(tt.m, &got, env(tt.env)); err != nil {
			t.Fatal(err)
		}
		if got.Name != tt.want {
			t.Errorf("Decode(%v) with env %v: Name = %q, want %q", tt.m, tt.env, got.Name, tt.want)
		}
	}
}

func TestDecodeErrors(t *testing.T) {
	tests := []struct {
		m       map[string]any
		wantErr string
	}{
		{map[string]any{"unknown": 1, "other": 2}, "unknown keys: [other unknown] (available: [name mode"},
		{map[string]any{"nested": map[string]any{"x": 1}}, "unknown keys in nested: [x]"},
		{map[string]any{"name": 1}, "name must be a string"},
		{map[string]any{"mode": "c"}, "mode must be one of [a b]"},
		{map[string]any{"flag": "yes"}, "flag must be a boolean"},
		{map[string]any{"count": 1.5}, "count must be an integer"},
		{map[string]any{"count": "1"}, "count must be an integer"},
		{map[string]any{"count": int64(-1)}, "count must be at least 0"},
		{map[string]any{"attempts": int64(0)}, "attempts must be at least 1"},
		{map[string]any{"timeout": "soon"}, "timeout:"},
		{map[string]any{"timeout": int64(0)}, "timeout: must be positive"},
		{map[string]any{"size": "x"}, "size: must end with B"},
		{map[string]any{"size": "B"}, ""},
		{map[string]any{"tags": "x"}, "tags must be an array of strings"},
		{map[string]any{"tags": []any{"z"}}, "tags must contain only [x y]"},
		{map[string]any{"nested": true}, "nested must be an object"},
		{map[string]any{"nested": map[string]any{"rate": 1.5}}, "nested.rate must be at most 1"},
		{map[string]any{"nested": map[string]any{"rate": "1"}}, "nested.rate must be a number"},
	}
	for _, tt := range tests {
		var got target
		err := Decode(tt.m, &got, nil)
		if tt.wantErr == "" {
			if err != nil {
				t.Errorf("Decode(%v) error = %v", tt.m, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
			t.Errorf("Decode(%v) error = %v, want %q", tt.m, err, tt.wantErr)
		}
	}

	// All errors are reported.
	err := Decode(map[string]any{"name": 1, "flag": 1}, &target{}, nil)
	if err == nil || !strings.Contains(err.Error(), "name") || !strings.Contains(err.Error(), "flag") {
		t.Errorf("error = %v", err)
	}
	if err := Decode(map[string]any{}, target{}, nil); err == nil {
		t.Error("Decode into a non-pointer did not fail")
	}
}

func TestKeys(t *testing.T) {
	got := Keys(reflect.TypeFor[nested]())
	if !reflect.DeepEqual(got, []string{"rate", "on"}) {
		t.Errorf("Keys() = %v", got)
	}
}
