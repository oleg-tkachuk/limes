package limes

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/limes/dpop"
)

// dpopDir is package dpop's source, beside this one.
const dpopDir = "dpop"

// dpopSentinels are package dpop's error sentinels, by name. A sentinel added
// there and missing here fails TestDPoPSentinelsAreListed.
var dpopSentinels = map[string]error{
	"ErrRequired":      dpop.ErrRequired,
	"ErrInvalid":       dpop.ErrInvalid,
	"ErrReplayed":      dpop.ErrReplayed,
	"ErrNonceRequired": dpop.ErrNonceRequired,
}

// sentinelNames is every package-level variable of dir's non-test sources
// whose name starts with Err.
func sentinelNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var names []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
				for _, name := range spec.(*ast.ValueSpec).Names {
					if strings.HasPrefix(name.Name, "Err") {
						names = append(names, name.Name)
					}
				}
			}
		}
	}
	slices.Sort(names)
	return names
}

func TestDPoPSentinelsAreListed(t *testing.T) {
	var listed []string
	for name := range dpopSentinels {
		listed = append(listed, name)
	}
	slices.Sort(listed)
	if got := sentinelNames(t, dpopDir); !slices.Equal(got, listed) {
		t.Fatalf("package dpop declares %v, dpopSentinels lists %v", got, listed)
	}
}

// Verify reads a dpop error as a refused proof through proofRefusals, and
// ReasonOf and ErrorResponse answer it through refusalRules: a sentinel in
// one and not the other is either a refusal answered as a server failure or
// a store failure answered as a bad proof.
func TestProofRefusalsAgreeWithRefusalRules(t *testing.T) {
	for name, sentinel := range dpopSentinels {
		if !slices.Contains(proofRefusals, sentinel) {
			t.Errorf("dpop.%s is missing from proofRefusals", name)
		}
		i := slices.IndexFunc(refusalRules, func(r refusalRule) bool { return errors.Is(r.sentinel, sentinel) })
		if i < 0 || refusalRules[i].reason == "" {
			t.Errorf("dpop.%s has no refusal rule with a reason", name)
		}
	}
	sentinels := slices.Collect(maps.Values(dpopSentinels))
	for _, r := range proofRefusals {
		if !slices.Contains(sentinels, r) {
			t.Errorf("proofRefusals holds %v, which is no dpop sentinel", r)
		}
	}
}
