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

var (
	// defaultsSection opens a module's table.
	defaultsSection = regexp.MustCompile(`^## (\S+)$`)
	// defaultsRow is one row: package.Config, field, what zero does.
	defaultsRow = regexp.MustCompile("^\\| `([a-z]+)\\.([A-Z]\\w*)` \\| `([A-Z]\\w*)` \\| (.+) \\|$")
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
	module  string
	configs map[string][]string
	names   map[string]bool
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
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		p := pkgs[f.Name.Name]
		if p == nil {
			p = &repoPackage{module: moduleOf(path), configs: map[string][]string{}, names: map[string]bool{}}
			pkgs[f.Name.Name] = p
		}
		collect(f, p)
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

func collect(f *ast.File, p *repoPackage) {
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gen.Specs {
			switch s := spec.(type) {
			case *ast.ValueSpec:
				for _, n := range s.Names {
					p.names[n.Name] = true
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
	module, zero string
}

func readDefaultsDoc(t *testing.T) map[string]defaultsDocRow {
	t.Helper()
	raw, err := os.ReadFile(defaultsDoc)
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]defaultsDocRow{}
	module := ""
	for line := range strings.Lines(string(raw)) {
		line = strings.TrimRight(line, "\n")
		if m := defaultsSection.FindStringSubmatch(line); m != nil {
			module = m[1]
			continue
		}
		m := defaultsRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		key := m[1] + "." + m[2] + "." + m[3]
		if _, dup := rows[key]; dup {
			t.Errorf("%s lists %s twice", defaultsDoc, key)
		}
		rows[key] = defaultsDocRow{module: module, zero: m[4]}
	}
	return rows
}

// docs/defaults.md has a row for every exported field of every exported
// Config struct, under its module, and no row for a field there is not.
func TestDefaultsDocListsEveryConfigField(t *testing.T) {
	pkgs := readRepoPackages(t)
	rows := readDefaultsDoc(t)
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

// Every Default… constant a row cites is declared in the package it names,
// or in the row's own when it names none.
func TestDefaultsDocCitesDeclaredConstants(t *testing.T) {
	pkgs := readRepoPackages(t)
	cited := 0
	for key, row := range readDefaultsDoc(t) {
		own, _, _ := strings.Cut(key, ".")
		for _, m := range defaultConstant.FindAllStringSubmatch(row.zero, -1) {
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
