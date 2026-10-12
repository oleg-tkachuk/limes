package limes_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/memstore"
)

// limesDir is the directory of package limes, which the tests run in.
const limesDir = "."

// interfaceMethods reads every interface package limes declares, with the
// methods of the interfaces it embeds, keyed by interface name.
func interfaceMethods(t *testing.T) map[string][]string {
	t.Helper()
	fset := token.NewFileSet()
	paths, err := filepath.Glob(filepath.Join(limesDir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	decls := map[string]*ast.InterfaceType{}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if ts, ok := n.(*ast.TypeSpec); ok {
				if it, ok := ts.Type.(*ast.InterfaceType); ok {
					decls[ts.Name.Name] = it
				}
			}
			return true
		})
	}
	var methods func(name string) []string
	methods = func(name string) []string {
		var out []string
		for _, field := range decls[name].Methods.List {
			if len(field.Names) > 0 {
				for _, n := range field.Names {
					out = append(out, n.Name)
				}
				continue
			}
			embedded := field.Type
			if ix, ok := embedded.(*ast.IndexExpr); ok { // a generic, as Meter[TX]
				embedded = ix.X
			}
			if id, ok := embedded.(*ast.Ident); ok {
				if _, local := decls[id.Name]; local {
					out = append(out, methods(id.Name)...)
				}
			}
		}
		return out
	}
	all := map[string][]string{}
	for name := range decls {
		if ast.IsExported(name) {
			all[name] = methods(name)
		}
	}
	return all
}

func hasMethods(t reflect.Type, names []string) bool {
	for _, n := range names {
		if _, ok := t.MethodByName(n); !ok {
			return false
		}
	}
	return len(names) > 0
}

// The decorator rule: Instrument forwards every interface of package limes
// the store it wraps implements. An optional interface added to limes and to
// memstore, but not to the decorator, fails here rather than vanishing behind
// a wrapped store.
func TestInstrumentForwardsEveryInterfaceTheStoreImplements(t *testing.T) {
	inner, err := memstore.NewUsageStore(memstore.UsageStoreConfig[struct{}]{Records: newMemstoreRecords(t)})
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := limes.Instrument[struct{}](inner)
	if err != nil {
		t.Fatal(err)
	}
	innerType, wrappedType := reflect.TypeOf(inner), reflect.TypeOf(wrapped)
	var checked []string
	for name, methods := range interfaceMethods(t) {
		if !hasMethods(innerType, methods) {
			continue
		}
		checked = append(checked, name)
		if !hasMethods(wrappedType, methods) {
			t.Errorf("Instrument hides %s, which the wrapped store implements", name)
		}
	}
	// The rule is only pinned if it reaches the optional interface.
	const optional = "CopyUsageReader"
	if !slices.Contains(checked, optional) {
		t.Fatalf("checked %v, want %s among them", checked, optional)
	}
}

// newMemstoreRecords is an empty memstore record store, for a usage store
// to find lineage in.
func newMemstoreRecords(t *testing.T) *memstore.Store {
	t.Helper()
	records, err := memstore.NewStore(memstore.StoreConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return records
}
