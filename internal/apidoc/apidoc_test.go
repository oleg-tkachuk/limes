package apidoc

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// sampleDir is the fixture package, from the repository's root.
const sampleDir = "internal/apidoc/testdata/sample"

// renderSample builds the fixture package as the reference would, in the
// capabilities category.
func renderSample(t *testing.T) Section {
	t.Helper()
	p, err := parse(repoRoot, Package{Dir: sampleDir, Category: catCapabilities})
	if err != nil {
		t.Fatal(err)
	}
	r := &renderer{names: map[string]string{p.importPath: p.doc.Name}, files: map[string]string{}}
	index, err := r.build([]*parsed{p})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range index.Categories {
		if c.ID == catCapabilities {
			if len(c.Sections) != 1 {
				t.Fatalf("capabilities holds %d sections, want the fixture's", len(c.Sections))
			}
			return c.Sections[0]
		}
	}
	t.Fatal("no capabilities category")
	return Section{}
}

// The fixture's declarations: the constant, then the type with its
// constructor and method beneath it; an unexported field left out of the
// declaration; each doc link to the anchor of what it names, and one
// outside the reference to pkg.go.dev.
func TestSample(t *testing.T) {
	s := renderSample(t)
	if s.Anchor != packageAnchorPrefix+"sample" || !strings.Contains(s.Overview, "fixture of apidoc") {
		t.Errorf("section anchor %q, overview %q", s.Anchor, s.Overview)
	}
	var headings []string
	for _, it := range s.Items {
		headings = append(headings, it.Heading)
		for _, m := range it.Members {
			headings = append(headings, "  "+m.Heading)
		}
	}
	if want := []string{"sample.Limit", "sample.Thing", "  sample.NewThing", "  sample.Thing.Do"}; strings.Join(headings, "|") != strings.Join(want, "|") {
		t.Fatalf("headings %q, want %q", headings, want)
	}
	limit, thing := s.Items[0], s.Items[1]
	do := thing.Members[1]
	for _, c := range []struct{ name, got, want string }{
		{"Limit links Thing.Do", limit.Doc, "(#sample.Thing.Do)"},
		{"Limit links time.Duration outside", limit.Doc, "(https://pkg.go.dev/time/#Duration)"},
		{"Do links Limit", do.Doc, "(#sample.Limit)"},
		{"Thing shows its field's comment", thing.Decl, "// Name names it."},
		{"Thing hides its unexported field", thing.Decl, "unexported fields"},
		{"Do keeps its receiver", do.Decl, "func (t *Thing) Do() error"},
		{"Do names its source", do.SourceURL, sourcePrefix + sampleDir + "/sample.go"},
	} {
		if !strings.Contains(c.got, c.want) {
			t.Errorf("%s: %q lacks %q", c.name, c.got, c.want)
		}
	}
	if strings.Contains(thing.Decl, "hidden") {
		t.Errorf("Thing's declaration shows its unexported field:\n%s", thing.Decl)
	}
	if strings.Contains(do.Decl, "return") {
		t.Errorf("Do's declaration has its body:\n%s", do.Decl)
	}
}

// Every root file a category names declares something, so none is left
// over from a file since removed or emptied.
func TestRootFilesExist(t *testing.T) {
	for file := range rootFiles {
		if _, err := os.Stat(filepath.Join(repoRoot, file)); err != nil {
			t.Errorf("rootFiles names %s: %v", file, err)
		}
	}
	known := map[string]bool{}
	for _, c := range categories {
		known[c.ID] = true
	}
	for file, c := range rootFiles {
		if !known[c] {
			t.Errorf("rootFiles places %s in %q, no category", file, c)
		}
	}
	for _, p := range packages {
		if p.Category != "" && !known[p.Category] {
			t.Errorf("package %s is in %q, no category", p.Dir, p.Category)
		}
	}
}

// Each category holds at least one package, and each package its
// overview in exactly one file.
func TestEveryCategoryHoldsAPackage(t *testing.T) {
	files, err := Render(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range categories {
		body, ok := files[fileOf(c.ID)]
		if !ok || !strings.Contains(string(body), "## Package ") {
			t.Errorf("category %s has no package", c.ID)
		}
	}
	for _, p := range packages {
		name := filepath.Base(p.Dir)
		if p.Dir == rootDir {
			name = filepath.Base(ModulePath)
		}
		anchor := `<a id="` + packageAnchorPrefix + name + `"></a>`
		n := 0
		for _, body := range files {
			n += strings.Count(string(body), anchor)
		}
		if n != 1 {
			t.Errorf("package %s has its overview in %d files, want 1", p.Dir, n)
		}
	}
}

var (
	// anchorRE is an anchor the reference sets.
	anchorRE = regexp.MustCompile(`<a id="([^"]+)"></a>`)
	// linkRE is a Markdown link's target; an escaped bracket opens none.
	linkRE = regexp.MustCompile(`(?:^|[^\\])\[[^\]]*\]\(([^)\s]+)\)`)
)

// fence opens and closes a code block, and codeIndent starts an indented
// one's line.
const (
	fence      = "```"
	codeIndent = "    "
)

// prose is body without its code: the fenced blocks and the indented
// lines, where a bracket is Go, not a link.
func prose(body string) string {
	var out strings.Builder
	inFence := false
	for line := range strings.Lines(body) {
		switch {
		case strings.HasPrefix(line, fence):
			inFence = !inFence
		case inFence, strings.HasPrefix(line, "\t"), strings.HasPrefix(line, codeIndent):
		default:
			out.WriteString(line)
		}
	}
	return out.String()
}

// Every link of the reference reaches something: an anchor of its own
// file or another of the reference, a file of the repository, or the web.
func TestEveryLinkResolves(t *testing.T) {
	files, err := Render(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	anchors := map[string]map[string]bool{}
	for name, body := range files {
		anchors[name] = map[string]bool{}
		for _, m := range anchorRE.FindAllStringSubmatch(string(body), -1) {
			anchors[name][m[1]] = true
		}
	}
	dir := filepath.Join(repoRoot, filepath.FromSlash(OutputDir))
	for name, body := range files {
		for _, m := range linkRE.FindAllStringSubmatch(prose(string(body)), -1) {
			target := m[1]
			if strings.HasPrefix(target, "https://") {
				continue
			}
			file, id, _ := strings.Cut(target, "#")
			if file == "" {
				file = name
			}
			if have, ours := anchors[file]; ours {
				if id != "" && !have[id] {
					t.Errorf("%s links %s: no such anchor", name, target)
				}
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(file))); err != nil {
				t.Errorf("%s links %s: %v", name, target, err)
			}
		}
	}
}

// A package is in the module of the directory that holds it.
func TestModuleOf(t *testing.T) {
	for dir, want := range map[string]string{
		rootDir:               "limes",
		"meter":               "limes",
		"dpop/dpoptest":       "limes",
		"pgstore":             "pgstore",
		"pgstore/migrate/ddl": "pgstore",
		"exchange/jwtverify":  "exchange",
	} {
		if got := moduleOf(dir); got != want {
			t.Errorf("moduleOf(%q) = %q, want %q", dir, got, want)
		}
	}
}

// ModulePath is the root module's.
func TestModulePath(t *testing.T) {
	mod, err := os.ReadFile(filepath.Join(repoRoot, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(mod), "module "+ModulePath+"\n") && !strings.Contains(string(mod), "\nmodule "+ModulePath+"\n") {
		t.Errorf("go.mod does not declare module %s", ModulePath)
	}
}
