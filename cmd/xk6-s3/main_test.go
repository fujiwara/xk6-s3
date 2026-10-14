package main

import (
	"testing"

	s3 "github.com/fujiwara/xk6-s3"
	"go.k6.io/k6/v2/ext"
)

func TestSetVersion(t *testing.T) {
	get := func() string { return ext.Get(ext.JSExtension)[s3.ImportPath].Version }
	before := get()
	setVersion("")
	if got := get(); got != before {
		t.Errorf("setVersion(\"\") changed the version to %q", got)
	}
	setVersion("v1.2.3")
	if got := get(); got != "v1.2.3" {
		t.Errorf("version = %q, want %q", got, "v1.2.3")
	}
}
