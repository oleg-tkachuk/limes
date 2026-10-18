package otelmetric_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/limes/memstore"
	"github.com/oleg-tkachuk/limes/meter"
	"github.com/oleg-tkachuk/limes/otelmetric"
)

// meterPackage is the package whose interfaces Instrument forwards.
const meterPackage = "github.com/oleg-tkachuk/limes/meter"

// meterDir is the directory of the meter package this module builds
// against, as the go command resolves it.
func meterDir(t *testing.T) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "go", "list", "-f", "{{.Dir}}", meterPackage).Output()
	if err != nil {
		t.Fatalf("go list %s: %v", meterPackage, err)
	}
	return strings.TrimSpace(string(out))
}

// interfaceMethods reads every interface package meter declares, with the
// methods of the interfaces it embeds, keyed by interface name.
func interfaceMethods(t *testing.T) map[string][]string {
	t.Helper()
	fset := token.NewFileSet()
	paths, err := filepath.Glob(filepath.Join(meterDir(t), "*.go"))
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

// The decorator rule: Instrument forwards every interface of package meter
// the store it wraps implements. An optional interface added to meter and to
// memstore, but not to the decorator, fails here rather than vanishing behind
// a wrapped store.
func TestInstrumentForwardsEveryInterfaceTheStoreImplements(t *testing.T) {
	inner, err := memstore.NewUsageStore(memstore.UsageStoreConfig[struct{}]{Records: newMemstoreRecords(t)})
	if err != nil {
		t.Fatal(err)
	}
	// Every mix of the optional interfaces: the decorator claims each the
	// store has, and none it lacks.
	stores := map[string]meter.UsageStore[struct{}]{
		"every one": inner,
		"CopyUsageReader alone": struct {
			meter.UsageStore[struct{}]
			meter.CopyUsageReader
		}{inner, inner},
		"TenantUsageEraser alone": struct {
			meter.UsageStore[struct{}]
			meter.TenantUsageEraser
		}{inner, inner},
		"none": struct{ meter.UsageStore[struct{}] }{inner},
	}
	checked := map[string]bool{}
	for mix, store := range stores {
		wrapped, err := otelmetric.Instrument[struct{}](store)
		if err != nil {
			t.Fatal(err)
		}
		storeType, wrappedType := reflect.TypeOf(store), reflect.TypeOf(wrapped)
		for name, methods := range interfaceMethods(t) {
			has, shows := hasMethods(storeType, methods), hasMethods(wrappedType, methods)
			switch {
			case has && !shows:
				t.Errorf("%s: Instrument hides %s, which the wrapped store implements", mix, name)
			case shows && !has:
				t.Errorf("%s: Instrument claims %s, which the wrapped store lacks", mix, name)
			}
			checked[name] = checked[name] || has
		}
	}
	// The rule is only pinned if it reaches the optional interfaces.
	for _, optional := range []string{"CopyUsageReader", "TenantUsageEraser"} {
		if !checked[optional] {
			t.Errorf("no store checked implements %s", optional)
		}
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
