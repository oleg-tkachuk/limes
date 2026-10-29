// Package errguard finds the errors a module constructs without naming one
// of its sentinels (docs/adr/0012-error-codes.md): an errors.New, or an
// fmt.Errorf whose format wraps nothing (%w). Every error a component
// returns carries a code only when it is, or wraps, a sentinel; each
// module's test runs Bare over its sources and refuses what it finds,
// beside the helpers it lists whose every caller wraps their error.
package errguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// sentinelPrefix opens the name of an exported sentinel, which is where a
// family starts and may be a bare errors.New.
const sentinelPrefix = "Err"

// wrapVerb is the verb by which fmt.Errorf wraps an error.
const wrapVerb = "%w"

// Site is a bare construction: its file, and the function or package
// variable it is in.
type Site struct {
	File, Func string
	Line       int
}

// Bare is every bare construction in files, which it parses.
func Bare(files []string) ([]Site, error) { return find(files, bareCall) }

// Doubling is every fmt.Errorf in files whose text would name the module's
// prefix twice: a format that opens with it and wraps an error, which may
// open with it too, one that wraps two, or one that quotes an error's text.
// errs.Errorf names it once.
func Doubling(files []string) ([]Site, error) { return find(files, doublingCall) }

// find is every call in files that match reports, outside the declarations
// of exported sentinels.
func find(files []string, match func(*ast.CallExpr) bool) ([]Site, error) {
	var sites []Site
	fset := token.NewFileSet()
	for _, f := range files {
		file, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		for _, decl := range file.Decls {
			for _, node := range units(decl) {
				name, sentinel := unitName(node)
				if sentinel {
					continue
				}
				ast.Inspect(node, func(n ast.Node) bool {
					if call, ok := n.(*ast.CallExpr); ok && match(call) {
						sites = append(sites, Site{File: filepath.ToSlash(f), Func: name, Line: fset.Position(call.Pos()).Line})
					}
					return true
				})
			}
		}
	}
	return sites, nil
}

// units splits decl into what names a construction: a function, or each
// spec of a var block.
func units(decl ast.Decl) []ast.Node {
	gen, ok := decl.(*ast.GenDecl)
	if !ok {
		return []ast.Node{decl}
	}
	out := make([]ast.Node, len(gen.Specs))
	for i, spec := range gen.Specs {
		out[i] = spec
	}
	return out
}

// unitName is the name a unit goes by, and whether it declares exported
// sentinels, whose own errors.New starts a family.
func unitName(node ast.Node) (string, bool) {
	switch n := node.(type) {
	case *ast.FuncDecl:
		return n.Name.Name, false
	case *ast.ValueSpec:
		if len(n.Names) == 0 {
			return "(package)", false
		}
		first := n.Names[0]
		return first.Name, first.IsExported() && strings.HasPrefix(first.Name, sentinelPrefix)
	}
	return "(package)", false
}

// modulePrefix opens the text of every error the root module returns.
const modulePrefix = "limes: "

// errorMethod is the method that gives an error's text.
const errorMethod = "Error"

// doublingCall reports whether call is an fmt.Errorf whose literal format
// opens with modulePrefix and wraps, or wraps more than once, or that
// quotes an error's text.
func doublingCall(call *ast.CallExpr) bool {
	if !isCall(call, "fmt", "Errorf") || len(call.Args) == 0 {
		return false
	}
	for _, arg := range call.Args[1:] {
		if quotesError(arg) {
			return true
		}
	}
	lit, ok := leftmostLiteral(call.Args[0])
	if !ok {
		return false
	}
	format, err := strconv.Unquote(lit.Value)
	if err != nil {
		return false
	}
	wraps := strings.Count(format, wrapVerb)
	return wraps > 1 || wraps == 1 && strings.HasPrefix(format, modulePrefix)
}

// quotesError reports whether arg is x.Error(), the text of an error.
func quotesError(arg ast.Expr) bool {
	call, ok := arg.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == errorMethod
}

// isCall reports whether call is pkg.name(…).
func isCall(call *ast.CallExpr, pkg, name string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg && sel.Sel.Name == name
}

// bareCall reports whether call is errors.New, or fmt.Errorf with a format
// that wraps no error.
func bareCall(call *ast.CallExpr) bool {
	switch {
	case isCall(call, "errors", "New"):
		return true
	case isCall(call, "fmt", "Errorf"):
		if len(call.Args) == 0 {
			return true
		}
		lit, ok := leftmostLiteral(call.Args[0])
		if !ok {
			return true
		}
		format, err := strconv.Unquote(lit.Value)
		return err != nil || !strings.Contains(format, wrapVerb)
	}
	return false
}

// leftmostLiteral is the string literal a format opens with: the format
// itself, or the left end of a concatenation, as "%w: "+format.
func leftmostLiteral(e ast.Expr) (*ast.BasicLit, bool) {
	for {
		switch x := e.(type) {
		case *ast.BasicLit:
			return x, x.Kind == token.STRING
		case *ast.BinaryExpr:
			e = x.X
		default:
			return nil, false
		}
	}
}

// moduleFile marks a directory as a module of its own, checked by its own
// test.
const moduleFile = "go.mod"

// testSuffix ends a test file's name.
const testSuffix = "_test.go"

// Sources are the non-test Go files of the module rooted at root, every
// nested module, testdata and hidden directory left out, and every
// directory whose path, from root, starts with one of skip.
func Sources(root string, skip ...string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "." {
				return nil
			}
			if strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata" || hasPrefix(rel, skip) {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, moduleFile)); err == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(rel, ".go") && !strings.HasSuffix(rel, testSuffix) {
			files = append(files, rel)
		}
		return nil
	})
	return files, err
}

// hasPrefix reports whether path is, or is under, one of dirs.
func hasPrefix(path string, dirs []string) bool {
	for _, d := range dirs {
		if path == d || strings.HasPrefix(path, d+"/") {
			return true
		}
	}
	return false
}

// Refuse fails t for every bare construction in the module rooted at root
// but those wrapped lists — file, then function, then why every caller
// wraps it — and for a listed one that constructs none.
func Refuse(t testing.TB, root string, wrapped map[string]map[string]string, skip ...string) {
	t.Helper()
	files, err := Sources(root, skip...)
	if err != nil {
		t.Fatal(err)
	}
	full := make([]string, len(files))
	for i, f := range files {
		full[i] = filepath.Join(root, f)
	}
	sites, err := Bare(full)
	if err != nil {
		t.Fatal(err)
	}
	used := map[string]bool{}
	for _, s := range sites {
		rel, err := filepath.Rel(root, s.File)
		if err != nil {
			t.Fatal(err)
		}
		rel = filepath.ToSlash(rel)
		if _, ok := wrapped[rel][s.Func]; ok {
			used[rel+":"+s.Func] = true
			continue
		}
		t.Errorf("%s:%d (%s) constructs an error that names no sentinel: wrap limes.ErrInvalidRequest, ErrInvalidConfig, ErrInternal or the refusal it is", rel, s.Line, s.Func)
	}
	for file, funcs := range wrapped {
		for fn := range funcs {
			if !used[file+":"+fn] {
				t.Errorf("%s: %s is listed as a wrapped helper and constructs no bare error: drop it", file, fn)
			}
		}
	}
}

// RefuseDoubling fails t for every fmt.Errorf in the module rooted at root
// whose text would name its prefix twice (Doubling).
func RefuseDoubling(t testing.TB, root string, skip ...string) {
	t.Helper()
	files, err := Sources(root, skip...)
	if err != nil {
		t.Fatal(err)
	}
	for i, f := range files {
		files[i] = filepath.Join(root, f)
	}
	sites, err := Doubling(files)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sites {
		t.Errorf("%s:%d (%s) may name %q twice: build it with errs.Errorf", s.File, s.Line, s.Func, modulePrefix)
	}
}
