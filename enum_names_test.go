package limes

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// Every exported constant of an enum — a type of this package over a string
// or a uint8 — is named after its type in full: OverrunPolicyReject, not
// OverrunReject. One rule, so a reader finds a type's values by its name.
func TestEnumConstantsCarryTheirTypeName(t *testing.T) {
	fset := token.NewFileSet()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	enums := map[string]bool{}
	for _, f := range files {
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.TYPE {
				continue
			}
			for _, s := range g.Specs {
				ts := s.(*ast.TypeSpec)
				if id, ok := ts.Type.(*ast.Ident); ok && (id.Name == "string" || id.Name == "uint8") {
					enums[ts.Name.Name] = true
				}
			}
		}
	}
	if len(enums) == 0 {
		t.Fatal("found no enum types")
	}
	for _, f := range files {
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.CONST {
				continue
			}
			// A spec with no type of its own takes the one above it, as an
			// iota block's do.
			typ := ""
			for _, s := range g.Specs {
				vs := s.(*ast.ValueSpec)
				if id, ok := vs.Type.(*ast.Ident); ok {
					typ = id.Name
				} else if vs.Type != nil || len(vs.Values) > 0 {
					typ = ""
				}
				if !enums[typ] {
					continue
				}
				for _, n := range vs.Names {
					if n.IsExported() && !strings.HasPrefix(n.Name, typ) {
						t.Errorf("%s: constant %s of type %s is not named after it", fset.Position(n.Pos()), n.Name, typ)
					}
				}
			}
		}
	}
}
