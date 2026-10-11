// Command apidoc generates the API reference (docs/api.md) and the
// TypeScript declarations (index.d.ts) of the k6/x/s3 module from the Go
// source: the exported methods of Client, the options structs of the
// methods, Result and the client Config, with their doc comments and the
// struct tags interpreted by internal/jsobj. The layout of the files is in
// templates/.
//
// Run it with go generate in the repository root.
package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"
)

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	files, err := generate(*root)
	if err != nil {
		log.Fatal(err)
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(*root, f.path), f.content, 0o644); err != nil {
			log.Fatal(err)
		}
	}
}

type file struct {
	path    string
	content []byte
}

// generate returns the generated files relative to root.
func generate(root string) ([]file, error) {
	a, err := build(root)
	if err != nil {
		return nil, err
	}
	var files []file
	for path, tmpl := range map[string]string{
		"docs/api.md": "api.md.tmpl",
		"index.d.ts":  "index.d.ts.tmpl",
	} {
		content, err := render(a, tmpl)
		if err != nil {
			return nil, err
		}
		files = append(files, file{path, content})
	}
	return files, nil
}
