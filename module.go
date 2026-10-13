// Package s3 is a k6 extension for load testing S3-compatible object storage.
package s3

import (
	"crypto/rand"
	"errors"
	"os"
	"strings"
	"sync"

	"github.com/fujiwara/xk6-s3/internal/data"
	"github.com/fujiwara/xk6-s3/internal/metrics"
	"go.k6.io/k6/v2/js/common"
	"go.k6.io/k6/v2/js/modules"
)

//go:generate go run ./internal/cmd/apidoc

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

	metricsOnce sync.Once
	metrics     *metrics.Metrics
	metricsErr  error

	warnings warningLimiter
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
	r.metricsOnce.Do(func() {
		env := vu.InitEnv()
		if env == nil || env.Registry == nil {
			r.metricsErr = errors.New("xk6-s3 must be imported in the init context")
			return
		}
		r.metrics, r.metricsErr = metrics.Register(env.Registry)
	})
	return &ModuleInstance{vu: vu, root: r}
}

// Exports implements modules.Instance.
func (mi *ModuleInstance) Exports() modules.Exports {
	return modules.Exports{
		Named: map[string]any{
			"runId":     mi.RunID,
			"parseSize": mi.ParseSize,
			"Client":    mi.newClient,
		},
	}
}

// RunID returns the run ID shared by all VUs in the process, intended to be
// used as a key prefix such as `<runId>/<dataset>/<n>`. It is available in
// the init context. It is the value of the `XK6_S3_RUN_ID` environment
// variable if set, otherwise a random 8-character string. See the runId
// section of README.
func (mi *ModuleInstance) RunID() string {
	return mi.root.runID
}

// ParseSize returns the number of bytes of a size such as `"10MiB"` or
// `"1.5GiB"`, in the same format as the sizes of the operations (see the
// Sizes section of README), but not a size distribution. It throws on an
// invalid size. Use it to compare sizes with the bytes of results, for
// example to stop writing at a total size per VU:
//
//	const limit = s3.parseSize("1GiB");
//	let written = 0;
//
//	export default function () {
//	  if (written >= limit) {
//	    return;
//	  }
//	  const res = client.putObject("bucket", `${s3.runId()}/${__VU}-${__ITER}`, "10MiB");
//	  written += res.bytes;
//	}
func (mi *ModuleInstance) ParseSize(size string) int64 {
	n, err := data.ParseBytes(size)
	if err != nil {
		common.Throw(mi.vu.Runtime(), err)
	}
	return n
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
