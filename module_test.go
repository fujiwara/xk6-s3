package s3

import (
	"regexp"
	"testing"

	"go.k6.io/k6/v2/js/modulestest"
)

func TestResolveRunID(t *testing.T) {
	t.Run("from env", func(t *testing.T) {
		got := resolveRunID(func(key string) (string, bool) {
			if key == RunIDEnv {
				return "my-run", true
			}
			return "", false
		})
		if got != "my-run" {
			t.Errorf("resolveRunID() = %q, want %q", got, "my-run")
		}
	})

	t.Run("empty env falls back to random", func(t *testing.T) {
		got := resolveRunID(func(string) (string, bool) { return "", true })
		if !regexp.MustCompile(`^[a-z2-7]{8}$`).MatchString(got) {
			t.Errorf("resolveRunID() = %q, want 8 lowercase base32 chars", got)
		}
	})

	t.Run("random IDs differ", func(t *testing.T) {
		noEnv := func(string) (string, bool) { return "", false }
		if a, b := resolveRunID(noEnv), resolveRunID(noEnv); a == b {
			t.Errorf("resolveRunID() returned the same ID twice: %q", a)
		}
	})
}

func TestRunIDSharedAcrossVUs(t *testing.T) {
	root := New()

	rt1 := modulestest.NewRuntime(t)
	rt1.VU.InitEnvField.LookupEnv = func(key string) (string, bool) {
		if key == RunIDEnv {
			return "shared", true
		}
		return "", false
	}
	rt2 := modulestest.NewRuntime(t)
	rt2.VU.InitEnvField.LookupEnv = func(string) (string, bool) { return "", false }

	for _, rt := range []*modulestest.Runtime{rt1, rt2} {
		mi := root.NewModuleInstance(rt.VU)
		if err := rt.VU.Runtime().Set("s3", mi.Exports().Named); err != nil {
			t.Fatal(err)
		}
		v, err := rt.RunOnEventLoop(`s3.runId()`)
		if err != nil {
			t.Fatal(err)
		}
		if got := v.String(); got != "shared" {
			t.Errorf("s3.runId() = %q, want %q", got, "shared")
		}
	}
}
