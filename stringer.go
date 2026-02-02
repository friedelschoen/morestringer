// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

const usage = `Usage of morestringer:
	morestringer [flags] -type T [directory]
	morestringer [flags] -type T files... # Must be a single package
For more information, see:
	https://github.com/friedelschoen/morestringer
Flags:`

// baseName that will put the generated code together with pkg.
func baseName(pkg *Package, typename string) string {
	suffix := "string.go"
	if pkg.hasTestFiles {
		suffix = "string_test.go"
	}
	return fmt.Sprintf("%s_%s", strings.ToLower(typename), suffix)
}

// isDirectory reports whether the named file is a directory.
func isDirectory(name string) bool {
	info, err := os.Stat(name)
	if err != nil {
		log.Fatal(err)
	}
	return info.IsDir()
}

// File holds a single parsed file and associated data.
type File struct {
	pkg  *Package  // Package to which this file belongs.
	file *ast.File // Parsed AST.

	trimPrefix  string
	lineComment bool
	cNames      bool
}

type Package struct {
	name         string
	defs         map[*ast.Ident]types.Object
	files        []*File
	hasTestFiles bool
}

// loadPackages analyzes the single package constructed from the patterns and tags.
// loadPackages exits if there is an error.
//
// Returns all variants (such as tests) of the package.
//
// logf is a test logging hook. It can be nil when not testing.
func loadPackages(patterns, tags []string, trimPrefix string, lineComment, cNames bool) []*Package {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedSyntax | packages.NeedFiles,
		// Tests are included, let the caller decide how to fold them in.
		Tests:      true,
		BuildFlags: []string{fmt.Sprintf("-tags=%s", strings.Join(tags, " "))},
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		log.Fatal(err)
	}
	if len(pkgs) == 0 {
		log.Fatalf("error: no packages matching %v", strings.Join(patterns, " "))
	}

	out := make([]*Package, len(pkgs))
	for i, pkg := range pkgs {
		p := &Package{
			name:  pkg.Name,
			defs:  pkg.TypesInfo.Defs,
			files: make([]*File, len(pkg.Syntax)),
		}

		for j, file := range pkg.Syntax {
			p.files[j] = &File{
				file: file,
				pkg:  p,

				trimPrefix:  trimPrefix,
				lineComment: lineComment,
				cNames:      cNames,
			}
		}

		// Keep track of test files, since we might want to generated
		// code that ends up in that kind of package.
		// Can be replaced once https://go.dev/issue/38445 lands.
		for _, f := range pkg.GoFiles {
			if strings.HasSuffix(f, "_test.go") {
				p.hasTestFiles = true
				break
			}
		}

		out[i] = p
	}
	return out
}

func memPackage(source string, trimPrefix string, lineComment, cNames bool) *Package {
	fset := token.NewFileSet()
	fileast, err := parser.ParseFile(fset, "testsource.go", source, parser.ParseComments)
	if err != nil {
		log.Fatalf("error: unable to parse package: %v", err)
	}

	info := &types.Info{
		Defs: make(map[*ast.Ident]types.Object),
	}
	conf := types.Config{Importer: importer.Default()}
	pkg, err := conf.Check("p", fset, []*ast.File{fileast}, info)
	if err != nil {
		log.Fatalf("error: unable to check types: %v", err)
	}

	p := &Package{
		name: pkg.Name(),
		defs: info.Defs,
	}

	p.files = []*File{{
		pkg:  p,
		file: fileast,

		trimPrefix:  trimPrefix,
		lineComment: lineComment,
		cNames:      cNames,
	}}

	return p
}

func (pkg *Package) findValues(typeNames ...string) map[string][]Value {
	typeValues := make(map[string][]Value, len(typeNames))
	for _, name := range typeNames {
		typeValues[name] = nil
	}

	for _, file := range pkg.files {
		ast.Inspect(file.file, func(node ast.Node) bool {
			decl, ok := node.(*ast.GenDecl)
			if !ok || decl.Tok != token.CONST {
				// We only care about const declarations.
				return true
			}

			file.genDecl(decl, typeValues)
			return false
		})
	}
	return typeValues
}

// Value represents a declared constant.
type Value struct {
	original string // The name of the constant.
	repr     string // The representing name.
	// The value is stored as a bit pattern alone. The boolean tells us
	// whether to interpret it as an int64 or a uint64; the only place
	// this matters is when sorting.
	// Much of the time the str field is all we need; it is printed
	// by Value.String.
	value  uint64 // Will be converted to int64 when needed.
	signed bool   // Whether the constant is a signed type.
	str    string // The string representation given by the "go/constant" package.
}

func (v *Value) String() string {
	return v.str
}

func unwrapParen(e ast.Expr) ast.Expr {
	for e != nil {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
	return nil
}

func getCName(expr ast.Expr) string {
	astValue := unwrapParen(expr)
	if astValue == nil {
		return ""
	}
	ident, ok := astValue.(*ast.Ident)
	if !ok {
		return ""
	}
	name, ok := strings.CutPrefix(ident.Name, "_Ciconst_")
	if !ok {
		return ""
	}
	return name
}

func valueExpr(vspec *ast.ValueSpec, ni int) ast.Expr {
	if len(vspec.Values) == 0 {
		return nil
	}
	if len(vspec.Values) == 1 {
		return vspec.Values[0]
	}
	if ni < len(vspec.Values) {
		return vspec.Values[ni]
	}
	return nil
}

func (f *File) createValue(name string, cval constant.Value, signed bool, expr ast.Expr, comment *ast.CommentGroup) Value {
	v := Value{
		original: name,
		signed:   signed,
		str:      cval.String(),
	}
	if i64, ok := constant.Int64Val(cval); ok {
		v.value = uint64(i64)
	} else if u64, ok := constant.Uint64Val(cval); ok {
		v.value = u64
	} else {
		log.Fatalf("internal error: value of %s is not an integer: %s", name, cval.String())
	}

	if f.lineComment && comment != nil && len(comment.List) == 1 {
		v.repr = strings.TrimSpace(comment.Text())
	} else if cName := getCName(expr); f.cNames && cName != "" {
		v.repr = strings.TrimPrefix(cName, f.trimPrefix)
	} else {
		v.repr = strings.TrimPrefix(v.original, f.trimPrefix)
	}
	return v
}

// genDecl processes one declaration clause.
func (f *File) genDecl(decl *ast.GenDecl, typeValues map[string][]Value) {
	// The name of the type of the constants we are declaring.
	// Can change if this is a multi-element declaration.
	typ := ""
	// Loop over the elements of the declaration. Each element is a ValueSpec:
	// a list of names possibly followed by a type, possibly followed by values.
	// If the type and value are both missing, we carry down the type (and value,
	// but the "go/types" package takes care of that).
	for _, spec := range decl.Specs {
		vspec := spec.(*ast.ValueSpec) // Guaranteed to succeed as this is CONST.
		if vspec.Type == nil && len(vspec.Values) > 0 {
			// "X = 1". With no type but a value. If the constant is untyped,
			// skip this vspec and reset the remembered type.
			typ = ""

			// If this is a simple type conversion, remember the type.
			// We don't mind if this is actually a call; a qualified call won't
			// be matched (that will be SelectorExpr, not Ident), and only unusual
			// situations will result in a function call that appears to be
			// a type conversion.
			ce, ok := vspec.Values[0].(*ast.CallExpr)
			if !ok {
				continue
			}
			id, ok := ce.Fun.(*ast.Ident)
			if !ok {
				continue
			}
			typ = id.Name
		}
		if vspec.Type != nil {
			// "X T". We have a type. Remember it.
			ident, ok := vspec.Type.(*ast.Ident)
			if !ok {
				continue
			}
			typ = ident.Name
		}
		// check if this type is requested
		values, ok := typeValues[typ]
		if !ok {
			continue
		}
		// We now have a list of names (from one line of source code) all being
		// declared with the desired type.
		// Grab their names and actual values and store them in f.values.
		for ni, name := range vspec.Names {
			if name.Name == "_" {
				continue
			}
			// This dance lets the type checker find the values for us. It's a
			// bit tricky: look up the object declared by the name, find its
			// types.Const, and extract its value.
			obj, ok := f.pkg.defs[name]
			if !ok {
				log.Fatalf("no value for constant %s", name)
			}
			info := obj.Type().Underlying().(*types.Basic).Info()
			if info&types.IsInteger == 0 {
				log.Fatalf("can't handle non-integer constant type %s", typ)
			}
			value := obj.(*types.Const).Val() // Guaranteed to succeed as this is CONST.
			if value.Kind() != constant.Int {
				log.Fatalf("can't happen: constant is not an integer %s", name)
			}
			values = append(values, f.createValue(name.Name, value, info&types.IsUnsigned == 0, valueExpr(vspec, ni), vspec.Comment))
		}
		typeValues[typ] = values
	}
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("stringer: ")

	typeNames := flag.String("type", "", "comma-separated list of type names; must be set")
	output := flag.String("output", "", "output file name; default srcdir/<type>_string.go")
	trimprefix := flag.String("trimprefix", "", "trim the `prefix` from the generated constant names")
	linecomment := flag.Bool("linecomment", false, "use line comment text as printed text when present")
	buildTags := flag.String("tags", "", "comma-separated list of build tags to apply")
	cNames := flag.Bool("cnames", false, "constant is defined as C.*, use the C-name")
	genLookup := flag.String("lookup", "", "generate a lookup `function`, \"{}\" is replaced with type")
	genJson := flag.Bool("json", false, "generate JSONUnmarshal and JSONMarshal methods")

	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, usage)
		flag.PrintDefaults()
	}
	flag.Parse()
	if len(*typeNames) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	types := strings.Split(*typeNames, ",")
	var tags []string
	if len(*buildTags) > 0 {
		tags = strings.Split(*buildTags, ",")
	}

	// We accept either one directory or a list of files. Which do we have?
	args := flag.Args()
	if len(args) == 0 {
		// Default: process whole package in current directory.
		args = []string{"."}
	}

	// Parse the package once.
	var dir string
	// TODO(suzmue): accept other patterns for packages (directories, list of files, import paths, etc).
	if len(args) == 1 && isDirectory(args[0]) {
		dir = args[0]
	} else {
		if len(tags) != 0 {
			log.Fatal("-tags option applies only to directories, not when files are specified")
		}
		dir = filepath.Dir(args[0])
	}

	// For each type, generate code in the first package where the type is declared.
	// The order of packages is as follows:
	// package x
	// package x compiled for tests
	// package x_test
	//
	// Each package pass could result in a separate generated file.
	// These files must have the same package and test/not-test nature as the types
	// from which they were generated.
	//
	// Types will be excluded when generated, to avoid repetitions.
	pkgs := loadPackages(args, tags, *trimprefix, *linecomment, *cNames)
	sort.Slice(pkgs, func(i, j int) bool {
		// Put x_test packages last.
		iTest := strings.HasSuffix(pkgs[i].name, "_test")
		jTest := strings.HasSuffix(pkgs[j].name, "_test")
		if iTest != jTest {
			return !iTest
		}

		return len(pkgs[i].files) < len(pkgs[j].files)
	})
	g := Generator{
		lookup: *genLookup,
		json:   *genJson,
	}
	for _, pkg := range pkgs {
		g.buf.Reset()
		types = g.genPackage(pkg, types, dir, *output)
	}

	if len(types) > 0 {
		log.Fatalf("no values defined for types: %s", strings.Join(types, ","))
	}
}
