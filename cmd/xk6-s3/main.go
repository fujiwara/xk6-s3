// Command xk6-s3 is k6 with the xk6-s3 extension, as built by xk6.
package main

import (
	s3 "github.com/fujiwara/xk6-s3"
	k6cmd "go.k6.io/k6/v2/cmd"
	"go.k6.io/k6/v2/ext"
)

// version is the version of the release, set by GoReleaser with -ldflags.
var version string

func main() {
	setVersion(version)
	k6cmd.Execute()
}

// setVersion sets the version of the extension shown by `xk6-s3 version`.
// It is "(devel)" by default, because the extension is built from the same
// repository by the replace directive.
func setVersion(v string) {
	if v == "" {
		return
	}
	if e, ok := ext.Get(ext.JSExtension)[s3.ImportPath]; ok {
		e.Version = v
	}
}
