package detail

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWrapAndOf(t *testing.T) {
	base := errors.New("the server's text: kid k1, tenant 42")
	err := fmt.Errorf("outer: %w", Wrap(base, "the field is missing"))
	if got, ok := Of(err); !ok || got != "the field is missing" {
		t.Errorf("Of = %q, %v", got, ok)
	}
	if err.Error() != "outer: "+base.Error() || !errors.Is(err, base) {
		t.Errorf("the chain changed: %v", err)
	}
	if _, ok := Of(base); ok {
		t.Error("a detail on an error that carries none")
	}
	inner := Wrap(Wrap(base, "inner"), "outer")
	if got, _ := Of(inner); got != "outer" {
		t.Errorf("Of of two = %q, want the outermost", got)
	}
}

// moduleRoot is the repository's root, two directories up.
const moduleRoot = "../.."

// No code converts a value to Text: only a constant reaches one, by the
// implicit conversion every untyped string constant takes, so a detail
// never carries what a request, a token or a store held.
func TestNoTextConversions(t *testing.T) {
	err := filepath.WalkDir(moduleRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".")) && path != moduleRoot {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Text" {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "detail" {
					t.Errorf("%s: converts a value to detail.Text", path)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
