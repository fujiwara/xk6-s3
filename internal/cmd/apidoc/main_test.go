package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

const root = "../../.."

// TestGeneratedFilesUpToDate fails when the committed files differ from the
// Go source, so that the documents follow the code.
func TestGeneratedFilesUpToDate(t *testing.T) {
	files, err := generate(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		got, err := os.ReadFile(filepath.Join(root, f.path))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, f.content) {
			t.Errorf("%s is out of date; run go generate ./...", f.path)
		}
	}
}

func TestJSDoc(t *testing.T) {
	tests := []struct{ name, doc, want string }{
		{"Count", "Count is the number of objects.", "The number of objects."},
		{"Tags", "Tags are the tags.", "The tags."},
		{"PutObject", "PutObject uploads an object.", "Uploads an object."},
		{"Quiet", "Requests the quiet mode.", "Requests the quiet mode."},
	}
	for _, tt := range tests {
		if got := jsDoc(tt.name, tt.doc); got != tt.want {
			t.Errorf("jsDoc(%q, %q) = %q, want %q", tt.name, tt.doc, got, tt.want)
		}
	}
}

func TestAnchor(t *testing.T) {
	tests := []struct{ heading, want string }{
		{"`client.listObjects(bucket, prefix, options?)`", "clientlistobjectsbucket-prefix-options"},
		{"`new s3.Client(config)`", "new-s3clientconfig"},
		{"ClientConfig", "clientconfig"},
	}
	for _, tt := range tests {
		if got := anchor(tt.heading); got != tt.want {
			t.Errorf("anchor(%q) = %q, want %q", tt.heading, got, tt.want)
		}
	}
}

func TestReadmeLinks(t *testing.T) {
	got := readmeLinks("See the Request format section of README, and the\nSizes section of README.")
	want := "See [Request format](../README.md#request-format) in README, and [Sizes](../README.md#sizes) in README."
	if got != want {
		t.Errorf("readmeLinks() = %q, want %q", got, want)
	}
}

func TestFence(t *testing.T) {
	got := fence("For example:\n\n\tconst a = 1;\n\tif (a) {\n\t  f();\n\t}\n\nDone.")
	want := "For example:\n\n```js\nconst a = 1;\nif (a) {\n  f();\n}\n```\n\nDone."
	if got != want {
		t.Errorf("fence() = %q, want %q", got, want)
	}
}
