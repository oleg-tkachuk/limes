package limes

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// defaultsDoc lists what every Config field does at its zero value, one
// section per module (docs/compatibility.md: defaults are contract).
const defaultsDoc = "docs/defaults.md"

// rootModuleSection heads the root module's rows; a nested module's section
// is its directory's name.
const rootModuleSection = "limes"

// configSuffix ends the name of every struct whose fields the doc lists.
const configSuffix = "Config"

// defaultPrefix starts the name of every constant whose value the doc lists.
const defaultPrefix = "Default"

// helperSuffix ends the name of a package of test helpers, whose Default…
// constants tune a consumer's tests rather than what its code does.
const helperSuffix = "test"

// defaultWords are the words a row's Default cell may hold in place of a
// code span: what the zero field does when no value stands for it.
var defaultWords = []string{"required", "off", "on", "none", "refused"}

// defaultExprPackages are the packages a Default… constant's value may name
// besides the repository's: their values do not move under a release.
var defaultExprPackages = []string{"time"}

// defaultExprIdents are the predeclared names a Default… constant's value
// may hold.
var defaultExprIdents = []string{"string"}

var (
	// defaultsSection opens a module's table.
	defaultsSection = regexp.MustCompile(`^## (\S+)$`)
	// defaultsRow is one row: package.Config, field, the value or word a zero
	// field stands for, and what it does, in prose.
	defaultsRow = regexp.MustCompile("^\\| `([a-z]+)\\.([A-Z]\\w*)` \\| `([A-Z]\\w*)` \\| (.+?) \\| (.*?) ?\\|$")
	// defaultCell is what a Default cell holds: code spans, or one word.
	defaultCell = regexp.MustCompile("^(?:`[^`]+`(?:, `[^`]+`)*|[a-z]+)$")
	// constantRow is one row of a module's constants: package.Default…, and
	// its value as its declaration spells it.
	constantRow = regexp.MustCompile("^\\| `([a-z]+)\\.(Default[A-Z]\\w*)` \\| `([^`]+)` \\|$")
	// defaultConstant is a Default… name a row cites, with its package when
	// it is another's.
	defaultConstant = regexp.MustCompile("`(?:([a-z]+)\\.)?(Default[A-Z]\\w*)`")
)

// skippedDirs hold no package whose Config a consumer sets.
var skippedDirs = []string{".git", ".github", "examples", "internal", "testdata", "tools"}

// repoPackages is every package of the repository a consumer imports: its
// module's section in the doc, its exported Config structs' fields and the
// names it declares.
type repoPackage struct {
	module    string
	configs   map[string][]string
	names     map[string]bool
	constants map[string]ast.Expr
	sources   map[ast.Expr]string
}

func readRepoPackages(t *testing.T) map[string]*repoPackage {
	t.Helper()
	pkgs := map[string]*repoPackage{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if slices.Contains(skippedDirs, d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		p := pkgs[f.Name.Name]
		if p == nil {
			p = &repoPackage{
				module:    moduleOf(path),
				configs:   map[string][]string{},
				names:     map[string]bool{},
				constants: map[string]ast.Expr{},
				sources:   map[ast.Expr]string{},
			}
			pkgs[f.Name.Name] = p
		}
		collect(f, p, func(e ast.Expr) string {
			return string(src[fset.Position(e.Pos()).Offset:fset.Position(e.End()).Offset])
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return pkgs
}

// moduleOf is the doc section of the module path is in: the first
// directory above it holding a go.mod, the root's being rootModuleSection.
func moduleOf(path string) string {
	for dir := filepath.Dir(path); dir != "."; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
	}
	return rootModuleSection
}

// collect adds f's names, Config structs and Default… constants to p, each
// constant's value as source spells it.
func collect(f *ast.File, p *repoPackage, source func(ast.Expr) string) {
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gen.Specs {
			switch s := spec.(type) {
			case *ast.ValueSpec:
				for i, n := range s.Names {
					p.names[n.Name] = true
					if n.IsExported() && strings.HasPrefix(n.Name, defaultPrefix) && !strings.HasSuffix(f.Name.Name, helperSuffix) && i < len(s.Values) {
						p.constants[n.Name] = s.Values[i]
						p.sources[s.Values[i]] = source(s.Values[i])
					}
				}
			case *ast.TypeSpec:
				st, ok := s.Type.(*ast.StructType)
				if !ok || !s.Name.IsExported() || !strings.HasSuffix(s.Name.Name, configSuffix) {
					continue
				}
				var fields []string
				for _, field := range st.Fields.List {
					for _, n := range field.Names {
						if n.IsExported() {
							fields = append(fields, n.Name)
						}
					}
				}
				p.configs[s.Name.Name] = fields
			}
		}
	}
}

// defaultsDocRow is one row of the doc, where it stands.
type defaultsDocRow struct {
	module, value, zero string
}

// defaultsDocData is the doc's rows: of Config fields, by package.Config.Field,
// and of constants, by package.Default…, its value the row's.
type defaultsDocData struct {
	fields, constants map[string]defaultsDocRow
}

func readDefaultsDoc(t *testing.T) defaultsDocData {
	t.Helper()
	raw, err := os.ReadFile(defaultsDoc)
	if err != nil {
		t.Fatal(err)
	}
	doc := defaultsDocData{fields: map[string]defaultsDocRow{}, constants: map[string]defaultsDocRow{}}
	add := func(rows map[string]defaultsDocRow, key string, row defaultsDocRow) {
		if _, dup := rows[key]; dup {
			t.Errorf("%s lists %s twice", defaultsDoc, key)
		}
		rows[key] = row
	}
	module := ""
	for line := range strings.Lines(string(raw)) {
		line = strings.TrimRight(line, "\n")
		if m := defaultsSection.FindStringSubmatch(line); m != nil {
			module = m[1]
			continue
		}
		if m := defaultsRow.FindStringSubmatch(line); m != nil {
			add(doc.fields, m[1]+"."+m[2]+"."+m[3], defaultsDocRow{module: module, value: m[4], zero: m[5]})
		}
		if m := constantRow.FindStringSubmatch(line); m != nil {
			add(doc.constants, m[1]+"."+m[2], defaultsDocRow{module: module, value: m[3]})
		}
	}
	return doc
}

// docs/defaults.md has a row for every exported field of every exported
// Config struct, under its module, and no row for a field there is not.
func TestDefaultsDocListsEveryConfigField(t *testing.T) {
	pkgs := readRepoPackages(t)
	rows := readDefaultsDoc(t).fields
	fields := 0
	for pkg, p := range pkgs {
		for config, names := range p.configs {
			for _, field := range names {
				key := pkg + "." + config + "." + field
				fields++
				row, ok := rows[key]
				switch {
				case !ok:
					t.Errorf("%s has no row for %s", defaultsDoc, key)
				case row.module != p.module:
					t.Errorf("%s lists %s under %q, want %q", defaultsDoc, key, row.module, p.module)
				}
				delete(rows, key)
			}
		}
	}
	for key := range rows {
		t.Errorf("%s lists %s, which no Config has", defaultsDoc, key)
	}
	// The walk is only a check if it reaches the Configs, nested ones among them.
	for _, want := range []string{"limes.VerifierConfig", "pgstore.Config", "jwtverify.Config"} {
		pkg, config, _ := strings.Cut(want, ".")
		if p := pkgs[pkg]; p == nil || p.configs[config] == nil {
			t.Errorf("the walk found no %s", want)
		}
	}
	if fields == 0 {
		t.Fatal("the walk found no Config field")
	}
}

// Every row's Default cell is code spans or one of defaultWords: the one
// column a release compares, so prose stays free to be reworded.
func TestDefaultsDocCellsAreValues(t *testing.T) {
	for key, row := range readDefaultsDoc(t).fields {
		if !defaultCell.MatchString(row.value) {
			t.Errorf("%s: %s's Default cell is %q: want code spans or one word", defaultsDoc, key, row.value)
		} else if !strings.HasPrefix(row.value, "`") && !slices.Contains(defaultWords, row.value) {
			t.Errorf("%s: %s's Default cell is %q: want one of %q", defaultsDoc, key, row.value, defaultWords)
		}
	}
}

// docs/defaults.md lists every exported Default… constant of a package a
// consumer's code imports, under its module, with its value as declared: a
// change to the value changes the doc, which a release compares.
func TestDefaultsDocListsEveryConstant(t *testing.T) {
	pkgs := readRepoPackages(t)
	rows := readDefaultsDoc(t).constants
	for pkg, p := range pkgs {
		for name, expr := range p.constants {
			key := pkg + "." + name
			row, ok := rows[key]
			switch {
			case !ok:
				t.Errorf("%s has no row for %s", defaultsDoc, key)
			case row.module != p.module:
				t.Errorf("%s lists %s under %q, want %q", defaultsDoc, key, row.module, p.module)
			case row.value != p.sources[expr]:
				t.Errorf("%s lists %s as %q; it is %q", defaultsDoc, key, row.value, p.sources[expr])
			}
			delete(rows, key)
		}
	}
	for key := range rows {
		t.Errorf("%s lists %s, which is not declared", defaultsDoc, key)
	}
	if p := pkgs["pgstore"]; p == nil || p.constants["DefaultWatchHeartbeat"] == nil {
		t.Error("the walk found no pgstore.DefaultWatchHeartbeat")
	}
}

// A Default… constant's value names nothing whose change the doc would not
// show: only literals, other Default… constants, which have rows of their
// own, and defaultExprPackages.
func TestDefaultConstantsNameOnlyListedValues(t *testing.T) {
	pkgs := readRepoPackages(t)
	listed := func(pkg, name string) bool {
		p := pkgs[pkg]
		return p != nil && p.constants[name] != nil
	}
	for pkg, p := range pkgs {
		for name, expr := range p.constants {
			ast.Inspect(expr, func(n ast.Node) bool {
				switch e := n.(type) {
				case *ast.SelectorExpr:
					if x, ok := e.X.(*ast.Ident); ok && !slices.Contains(defaultExprPackages, x.Name) && !listed(x.Name, e.Sel.Name) {
						t.Errorf("%s.%s = %s names %s.%s, which has no row", pkg, name, p.sources[expr], x.Name, e.Sel.Name)
					}
					return false
				case *ast.Ident:
					if !slices.Contains(defaultExprIdents, e.Name) && !listed(pkg, e.Name) {
						t.Errorf("%s.%s = %s names %s, which has no row", pkg, name, p.sources[expr], e.Name)
					}
				}
				return true
			})
		}
	}
}

// Every Default… constant a row cites is declared in the package it names,
// or in the row's own when it names none.
func TestDefaultsDocCitesDeclaredConstants(t *testing.T) {
	pkgs := readRepoPackages(t)
	cited := 0
	for key, row := range readDefaultsDoc(t).fields {
		own, _, _ := strings.Cut(key, ".")
		for _, m := range defaultConstant.FindAllStringSubmatch(row.value+" "+row.zero, -1) {
			pkg := m[1]
			if pkg == "" {
				pkg = own
			}
			cited++
			if p := pkgs[pkg]; p == nil || !p.names[m[2]] {
				t.Errorf("%s: %s cites %s.%s, which is not declared", defaultsDoc, key, pkg, m[2])
			}
		}
	}
	if cited == 0 {
		t.Fatal("no row cites a Default constant")
	}
}
