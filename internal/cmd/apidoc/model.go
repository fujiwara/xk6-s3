package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// API is the JavaScript API of the module. The fields are exported for
// the templates.
type API struct {
	// Functions are the functions of the module.
	Functions []Method
	ClientDoc string
	Methods   []Method
	// Types are the interfaces in the order of appearance: the client
	// config, the options and Result.
	Types []*Interface
}

// Method is a function or a method exposed to JavaScript.
type Method struct {
	Name    string
	Params  []Param
	Returns string
	Doc     string
	// Options is the options interface, or nil.
	Options *Interface
}

// Param is a parameter of a method.
type Param struct {
	Name     string
	Type     string
	Optional bool
}

// Interface is an object type.
type Interface struct {
	Name   string
	Doc    string
	Fields []Field
	// Options reports whether the fields are optional keys of an object
	// passed to the module, rather than the fields of a result.
	Options bool
}

// Field is a key of an object type.
type Field struct {
	Name string
	Type string
	Doc  string
	// Default is the default in Markdown, or empty.
	Default string
	// Note describes the accepted values.
	Note string
}

// pkg is a parsed Go package.
type pkg struct {
	name    string
	types   map[string]*ast.TypeSpec
	docs    map[string]string
	methods map[string][]*ast.FuncDecl
}

func load(dir string) (*pkg, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, err
	}
	p := &pkg{types: map[string]*ast.TypeSpec{}, docs: map[string]string{}, methods: map[string][]*ast.FuncDecl{}}
	fset := token.NewFileSet()
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		p.name = f.Name.Name
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					p.types[ts.Name.Name] = ts
					doc := ts.Doc
					if doc == nil && len(d.Specs) == 1 {
						doc = d.Doc
					}
					p.docs[ts.Name.Name] = doc.Text()
				}
			case *ast.FuncDecl:
				if d.Recv == nil {
					continue
				}
				recv := d.Recv.List[0].Type
				if star, ok := recv.(*ast.StarExpr); ok {
					recv = star.X
				}
				if id, ok := recv.(*ast.Ident); ok {
					p.methods[id.Name] = append(p.methods[id.Name], d)
				}
			}
		}
	}
	return p, nil
}

// tsNames renames Go types in TypeScript, where Config would be ambiguous.
var tsNames = map[string]string{
	"Config":  "ClientConfig",
	"Tracing": "TracingConfig",
}

func tsName(goName string) string {
	if n, ok := tsNames[goName]; ok {
		return n
	}
	return goName
}

func build(root string) (*API, error) {
	s3, err := load(root)
	if err != nil {
		return nil, err
	}
	cl, err := load(filepath.Join(root, "internal", "client"))
	if err != nil {
		return nil, err
	}
	b := &builder{a: &API{}, seen: map[string]*Interface{}}

	for _, f := range moduleFunctions {
		d := find(s3.methods["ModuleInstance"], f.goName)
		if d == nil {
			return nil, fmt.Errorf("ModuleInstance.%s not found", f.goName)
		}
		m, err := b.method(s3, d, f.jsName)
		if err != nil {
			return nil, err
		}
		b.a.Functions = append(b.a.Functions, m)
	}
	if b.a.ClientDoc, err = docOf(s3, "Client"); err != nil {
		return nil, err
	}
	if _, err := b.iface(cl, "Config", true); err != nil {
		return nil, err
	}

	methods := slices.Clone(s3.methods["Client"])
	slices.SortFunc(methods, func(x, y *ast.FuncDecl) int { return strings.Compare(x.Name.Name, y.Name.Name) })
	for _, d := range methods {
		if !d.Name.IsExported() {
			continue
		}
		m, err := b.method(s3, d, lowerFirst(d.Name.Name))
		if err != nil {
			return nil, err
		}
		b.a.Methods = append(b.a.Methods, m)
	}
	if _, err := b.iface(s3, "Result", false); err != nil {
		return nil, err
	}
	return b.a, nil
}

// moduleFunctions are the methods of ModuleInstance exported as the
// functions of the module, in the order of the documents.
var moduleFunctions = []struct{ goName, jsName string }{
	{"RunID", "runId"},
	{"ParseSize", "parseSize"},
}

type builder struct {
	a *API
	// seen maps the qualified Go names of the converted structs.
	seen map[string]*Interface
}

func find(decls []*ast.FuncDecl, name string) *ast.FuncDecl {
	for _, d := range decls {
		if d.Name.Name == name {
			return d
		}
	}
	return nil
}

func docOf(p *pkg, name string) (string, error) {
	doc, ok := p.docs[name]
	if !ok {
		return "", fmt.Errorf("type %s not found", name)
	}
	if doc == "" {
		return "", fmt.Errorf("type %s has no doc comment", name)
	}
	return jsDoc(name, doc), nil
}

// method converts a method exposed to JavaScript as name.
func (b *builder) method(p *pkg, d *ast.FuncDecl, name string) (Method, error) {
	m := Method{Name: name, Doc: jsDoc(d.Name.Name, d.Doc.Text())}
	if d.Doc == nil {
		return m, fmt.Errorf("method %s has no doc comment", d.Name.Name)
	}
	for _, f := range d.Type.Params.List {
		for _, n := range f.Names {
			prm := Param{Name: n.Name}
			switch typ := exprString(f.Type); {
			case n.Name == "options" && typ == "sobek.Value":
				opts, err := b.iface(p, d.Name.Name+"Options", true)
				if err != nil {
					return m, fmt.Errorf("options of %s: %w", d.Name.Name, err)
				}
				prm.Type, prm.Optional, m.Options = opts.Name, true, opts
			default:
				t, ok := paramTypes[typ]
				if !ok {
					return m, fmt.Errorf("%s: unsupported parameter type %s of %s", d.Name.Name, typ, n.Name)
				}
				prm.Type = t
			}
			m.Params = append(m.Params, prm)
		}
	}
	if d.Type.Results == nil || len(d.Type.Results.List) != 1 {
		return m, fmt.Errorf("%s must return one value", d.Name.Name)
	}
	typ := exprString(d.Type.Results.List[0].Type)
	t, ok := paramTypes[typ]
	if !ok {
		return m, fmt.Errorf("%s: unsupported result type %s", d.Name.Name, typ)
	}
	m.Returns = t
	return m, nil
}

// paramTypes maps the Go types of parameters and results to TypeScript.
var paramTypes = map[string]string{
	"string":   "string",
	"int64":    "number",
	"[]string": "string[]",
	"Size":     "Size",
	"*Result":  "Result",
}

// iface converts a struct of p to an interface and adds it to the api once.
func (b *builder) iface(p *pkg, goName string, options bool) (*Interface, error) {
	ts, ok := p.types[goName]
	if !ok {
		return nil, fmt.Errorf("type %s not found", goName)
	}
	structType, ok := ts.Type.(*ast.StructType)
	if !ok {
		return nil, fmt.Errorf("type %s is not a struct", goName)
	}
	doc, err := docOf(p, goName)
	if err != nil {
		return nil, err
	}
	key := p.name + "." + goName
	if it, ok := b.seen[key]; ok {
		return it, nil
	}
	it := &Interface{Name: tsName(goName), Doc: doc, Options: options}
	b.seen[key] = it
	b.a.Types = append(b.a.Types, it)
	for _, f := range structType.Fields.List {
		if f.Tag == nil {
			continue
		}
		tag, err := strconv.Unquote(f.Tag.Value)
		if err != nil {
			return nil, err
		}
		st := reflect.StructTag(tag)
		js := st.Get("js")
		if js == "" {
			continue
		}
		goField := f.Names[0].Name
		if f.Doc == nil {
			return nil, fmt.Errorf("field %s.%s has no doc comment", goName, goField)
		}
		fd := Field{Name: js, Doc: jsDoc(goField, f.Doc.Text())}
		if fd.Type, fd.Note, err = b.fieldType(p, f.Type, st, options); err != nil {
			return nil, fmt.Errorf("field %s.%s: %w", goName, goField, err)
		}
		if !options {
			// Notes describe the accepted values, not the values of results.
			fd.Note = ""
		}
		fd.Default = defaultOf(st, fd.Type)
		it.Fields = append(it.Fields, fd)
	}
	return it, nil
}

// fieldType returns the TypeScript type of a field and a note on the
// accepted values.
func (b *builder) fieldType(p *pkg, expr ast.Expr, tag reflect.StructTag, options bool) (typ, note string, err error) {
	goType := exprString(expr)
	var enum []string
	if e, ok := tag.Lookup("enum"); ok {
		for v := range strings.SplitSeq(e, ",") {
			enum = append(enum, strconv.Quote(v))
		}
	}
	bounds := func(integer bool) string {
		lo, hasMin := tag.Lookup("min")
		hi, hasMax := tag.Lookup("max")
		if integer && !hasMin {
			lo, hasMin = "0", true
		}
		var s string
		switch {
		case hasMin && hasMax:
			s = fmt.Sprintf("in [%s, %s]", lo, hi)
		case hasMin:
			s = "at least " + lo
		case hasMax:
			s = "at most " + hi
		}
		if integer {
			return strings.TrimSpace("An integer " + s + ".")
		}
		if s != "" {
			return "A number " + s + "."
		}
		return ""
	}
	if ts, ok := tag.Lookup("ts"); ok {
		return ts, "", nil
	}
	switch goType {
	case "string":
		if enum != nil {
			return strings.Join(enum, " | "), "", nil
		}
		return "string", "", nil
	case "[]string":
		if enum != nil {
			return "(" + strings.Join(enum, " | ") + ")[]", "", nil
		}
		return "string[]", "", nil
	case "bool":
		return "boolean", "", nil
	case "int", "int64":
		return "number", bounds(true), nil
	case "float64":
		return "number", bounds(false), nil
	case "time.Duration":
		return "string | number", "A duration string such as `\"30s\"` or milliseconds.", nil
	case "data.Fixed":
		n := "A size such as `\"5MiB\"` or a number of bytes, but not a size distribution."
		if lo, ok := tag.Lookup("min"); ok {
			unit := " bytes."
			if lo == "1" {
				unit = " byte."
			}
			n += " At least " + lo + unit
		}
		return "string | number", n, nil
	}
	if id, ok := expr.(*ast.Ident); ok {
		if _, ok := p.types[id.Name]; ok {
			it, err := b.iface(p, id.Name, options)
			if err != nil {
				return "", "", err
			}
			return it.Name, "", nil
		}
	}
	return "", "", fmt.Errorf("unsupported type %s (set a ts tag)", goType)
}

// defaultOf returns the default of a field in Markdown, or empty.
func defaultOf(tag reflect.StructTag, typ string) string {
	var parts []string
	if env, ok := tag.Lookup("env"); ok {
		for v := range strings.SplitSeq(env, ",") {
			parts = append(parts, "`"+v+"`")
		}
	}
	if def, ok := tag.Lookup("default"); ok {
		if strings.Contains(typ, "string") || strings.HasPrefix(typ, `"`) {
			def = strconv.Quote(def)
		}
		parts = append(parts, "`"+def+"`")
	}
	return strings.Join(parts, ", then ")
}

func exprString(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return exprString(t.X) + "." + t.Sel.Name
	case *ast.StarExpr:
		return "*" + exprString(t.X)
	case *ast.ArrayType:
		return "[]" + exprString(t.Elt)
	}
	return fmt.Sprintf("%T", e)
}

// jsDoc converts a Go doc comment of goName for JavaScript users: the
// leading name ("Count is the number ..." or "PutObject uploads ...") is
// removed, and code blocks are fenced as JavaScript.
func jsDoc(goName, doc string) string {
	doc = fence(strings.TrimSpace(doc))
	for _, prefix := range []string{goName + " is ", goName + " are ", goName + " "} {
		if rest, ok := strings.CutPrefix(doc, prefix); ok {
			return upperFirst(rest)
		}
	}
	return doc
}

// fence converts the tab-indented code blocks of a doc comment to fenced
// JavaScript code blocks.
func fence(doc string) string {
	var out []string
	inCode := false
	for line := range strings.SplitSeq(doc, "\n") {
		code, isCode := strings.CutPrefix(line, "\t")
		switch {
		case isCode && !inCode:
			out = append(out, "```js")
			inCode = true
		case !isCode && inCode && line != "":
			// Blank lines at the end of the block go after the fence.
			n := len(out)
			for out[n-1] == "" {
				n--
			}
			out = append(append(out[:n], "```"), make([]string, len(out)-n)...)
			inCode = false
		}
		if inCode && line == "" {
			// A blank line in a code block is kept in the block.
			out = append(out, "")
			continue
		}
		if inCode {
			line = code
		}
		out = append(out, line)
	}
	if inCode {
		out = append(out, "```")
	}
	return strings.Join(out, "\n")
}

func upperFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}

func lowerFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToLower(r)) + s[n:]
}
