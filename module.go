// Package s3 is a k6 extension for load testing S3-compatible object storage.
package s3

import (
	"crypto/rand"
	"os"
	"strings"
	"sync"

	"go.k6.io/k6/v2/js/modules"
)

// ImportPath is the JavaScript import path of the module.
const ImportPath = "k6/x/s3"

// RunIDEnv is the environment variable that overrides the run ID.
const RunIDEnv = "XK6_S3_RUN_ID"

const runIDLength = 8

func init() {
	modules.Register(ImportPath, New())
}

// RootModule is the global module instance shared by all VUs in a process.
type RootModule struct {
	runIDOnce sync.Once
	runID     string
}

// ModuleInstance is the per-VU module instance.
type ModuleInstance struct {
	vu   modules.VU
	root *RootModule
}

var (
	_ modules.Module   = &RootModule{}
	_ modules.Instance = &ModuleInstance{}
)

// New returns a new RootModule.
func New() *RootModule {
	return &RootModule{}
}

// NewModuleInstance implements modules.Module.
// k6 calls it in the init context of each VU.
func (r *RootModule) NewModuleInstance(vu modules.VU) modules.Instance {
	r.runIDOnce.Do(func() {
		r.runID = resolveRunID(lookupEnvFunc(vu))
	})
	return &ModuleInstance{vu: vu, root: r}
}

// Exports implements modules.Instance.
func (mi *ModuleInstance) Exports() modules.Exports {
	return modules.Exports{
		Named: map[string]any{
			"runId": mi.RunID,
		},
	}
}

// RunID returns the run ID shared by all VUs in the process.
func (mi *ModuleInstance) RunID() string {
	return mi.root.runID
}

func lookupEnvFunc(vu modules.VU) func(string) (string, bool) {
	if env := vu.InitEnv(); env != nil && env.LookupEnv != nil {
		return env.LookupEnv
	}
	return os.LookupEnv
}

// resolveRunID returns the value of RunIDEnv if set, or a short random string.
func resolveRunID(lookupEnv func(string) (string, bool)) string {
	if v, ok := lookupEnv(RunIDEnv); ok && v != "" {
		return v
	}
	return strings.ToLower(rand.Text()[:runIDLength])
}
