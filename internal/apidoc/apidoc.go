// Package apidoc generates docs/api, the reference of every exported name
// of the repository's public packages, from their doc comments: the
// comments are the specification (docs/compatibility.md, "Documented
// behaviour"), so the reference repeats them rather than restating them,
// and a test holds the directory to what this package generates.
//
// The reference is an index and one file per category: one file of every
// package's declarations is past the size GitHub renders.
package apidoc

import (
	"bytes"
	_ "embed"
	"fmt"
	"go/ast"
	"go/doc"
	"go/doc/comment"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"strings"
	"text/template"
)

// ModulePath is the root module's path, which every package's import path
// starts with.
const ModulePath = "github.com/oleg-tkachuk/limes"

// OutputDir is the directory the reference is written to, from the
// repository's root, and IndexFile its index within it.
const (
	OutputDir = "docs/api"
	IndexFile = "README.md"
)

// categoryFileExt ends each category's file, named by the category's ID.
const categoryFileExt = ".md"

// sourcePrefix leads a source link from OutputDir back to the repository's
// root.
const sourcePrefix = "../../"

// Heading levels: a declaration's comment is under its H3 or H4 heading,
// a package's overview under its H3.
const (
	declHeadingLevel     = 5
	overviewHeadingLevel = 4
)

// pkgGoDev is where a doc link to a package outside the reference points.
const pkgGoDev = "https://pkg.go.dev/"

// packageAnchorPrefix starts the anchor of a package's overview.
const packageAnchorPrefix = "package-"

// The templates of the template file: the index and a category's file.
const (
	indexTemplate    = "index"
	categoryTemplate = "category"
)

//go:embed api.md.tmpl
var pageTemplate string

// Item is one documented declaration: a constant or variable group, a
// function, a type, or a type's member.
type Item struct {
	// Anchors are the ids a doc link reaches it by, one per name it
	// declares.
	Anchors []string
	Heading string
	Decl    string
	Doc     string
	// Source is the declaring file from the repository's root, and
	// SourceURL the link to it from OutputDir.
	Source    string
	SourceURL string
	Members   []Item

	// text is the doc comment, rendered into Doc once every anchor's file
	// is known; b is the package that declares it.
	text string
	b    *builder
}

// Section is one package's part of a category: its overview where the
// category is the package's first, and its declarations.
type Section struct {
	Name       string
	ImportPath string
	Anchor     string
	Overview   string
	Items      []Item
}

// CategoryPage is a category with the sections that fall in it.
type CategoryPage struct {
	Category
	File     string
	Sections []Section
}

// PackagePage is one row of the index's package table.
type PackagePage struct {
	Name       string
	ImportPath string
	Module     string
	// Link reaches the package's overview, in the file of its first
	// category.
	Link       string
	Synopsis   string
	Categories []CategoryPage
}

// Index is what the index template renders.
type Index struct {
	Packages   []PackagePage
	Categories []CategoryPage
}

// itemAt is an item and the heading it is rendered under.
type itemAt struct {
	Item  Item
	Level string
}

// funcs are the template's helpers.
var funcs = template.FuncMap{
	"at": func(it Item, level string) itemAt { return itemAt{it, level} },
}

// parsed is one package as parsed: its files by base name, and its doc.
type parsed struct {
	Package
	importPath string
	module     string
	fset       *token.FileSet
	files      map[string]*ast.File
	doc        *doc.Package
}

// Render generates the reference from the packages under root, the
// repository's root: each file of OutputDir by its name.
func Render(root string) (map[string][]byte, error) {
	pkgs := make([]*parsed, 0, len(packages))
	for _, p := range packages {
		pp, err := parse(root, p)
		if err != nil {
			return nil, err
		}
		pkgs = append(pkgs, pp)
	}
	r := &renderer{names: map[string]string{}, files: map[string]string{}}
	for _, p := range pkgs {
		r.names[p.importPath] = p.doc.Name
	}
	index, err := r.build(pkgs)
	if err != nil {
		return nil, err
	}
	tmpl, err := template.New(OutputDir).Funcs(funcs).Parse(pageTemplate)
	if err != nil {
		return nil, fmt.Errorf("apidoc: template: %w", err)
	}
	out := map[string][]byte{}
	exec := func(file, name string, data any) error {
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
			return fmt.Errorf("apidoc: render %s: %w", file, err)
		}
		out[file] = buf.Bytes()
		return nil
	}
	if err := exec(IndexFile, indexTemplate, index); err != nil {
		return nil, err
	}
	for _, c := range index.Categories {
		if err := exec(c.File, categoryTemplate, c); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// parse reads the package in p.Dir under root, its tests left out.
func parse(root string, p Package) (*parsed, error) {
	dir := filepath.Join(root, filepath.FromSlash(p.Dir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("apidoc: %s: %w", p.Dir, err)
	}
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	var list []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("apidoc: %w", err)
		}
		files[name] = f
		list = append(list, f)
	}
	importPath := ModulePath
	if p.Dir != rootDir {
		importPath = path.Join(ModulePath, p.Dir)
	}
	d, err := doc.NewFromFiles(fset, list, importPath)
	if err != nil {
		return nil, fmt.Errorf("apidoc: %s: %w", p.Dir, err)
	}
	return &parsed{Package: p, importPath: importPath, module: moduleOf(p.Dir), fset: fset, files: files, doc: d}, nil
}

// moduleOf is the module that holds the package in dir: a nested module
// is named by its directory, the root module by its path's last element.
func moduleOf(dir string) string {
	first, _, _ := strings.Cut(dir, "/")
	if _, nested := nestedModules[first]; nested {
		return first
	}
	return path.Base(ModulePath)
}

// nestedModules are the directories that hold a module of their own.
var nestedModules = map[string]struct{}{
	"pgstore": {}, "redisreplay": {}, "otelmetric": {}, "exchange": {}, "biscuit": {},
}

// renderer holds what a doc link is resolved by: each package's name by
// its import path, and the file of each anchor.
type renderer struct {
	names map[string]string
	files map[string]string
}

// builder collects one package's declarations.
type builder struct {
	*parsed
	r *renderer
}

// fileOf is the file a category is written to.
func fileOf(categoryID string) string { return categoryID + categoryFileExt }

// build assembles the index and the categories, then renders every doc
// comment, once the file of every anchor is known.
func (r *renderer) build(pkgs []*parsed) (Index, error) {
	byID := map[string]*CategoryPage{}
	index := Index{Categories: make([]CategoryPage, len(categories))}
	for i, c := range categories {
		index.Categories[i] = CategoryPage{Category: c, File: fileOf(c.ID)}
		byID[c.ID] = &index.Categories[i]
	}
	// An overview is rendered once every anchor is placed: its category,
	// a pointer into index.Categories, which no append moves, and the
	// section's index in it.
	type overview struct {
		b       *builder
		page    *CategoryPage
		section int
	}
	var overviews []overview
	for _, p := range pkgs {
		b := &builder{parsed: p, r: r}
		items, err := b.byCategory()
		if err != nil {
			return Index{}, err
		}
		pp := PackagePage{Name: p.doc.Name, ImportPath: p.importPath, Module: p.module, Synopsis: p.doc.Synopsis(p.doc.Doc)}
		for _, c := range categories {
			its, ok := items[c.ID]
			if !ok {
				continue
			}
			cp := byID[c.ID]
			s := Section{Name: p.doc.Name, ImportPath: p.importPath, Items: its}
			if len(pp.Categories) == 0 {
				s.Anchor = packageAnchorPrefix + p.doc.Name
				r.files[s.Anchor] = cp.File
				pp.Link = cp.File + "#" + s.Anchor
			}
			cp.Sections = append(cp.Sections, s)
			if s.Anchor != "" {
				overviews = append(overviews, overview{b, cp, len(cp.Sections) - 1})
			}
			pp.Categories = append(pp.Categories, CategoryPage{Category: c, File: cp.File})
			for _, it := range its {
				r.place(it, cp.File)
			}
		}
		index.Packages = append(index.Packages, pp)
	}
	for _, o := range overviews {
		s := &o.page.Sections[o.section]
		s.Overview = o.b.markdown(o.b.doc.Doc, overviewHeadingLevel, o.page.File)
	}
	for i := range index.Categories {
		c := &index.Categories[i]
		for j := range c.Sections {
			r.renderDocs(c.Sections[j].Items, c.File)
		}
	}
	return index, nil
}

// place records the file of its anchors and its members'.
func (r *renderer) place(it Item, file string) {
	for _, a := range it.Anchors {
		r.files[a] = file
	}
	for _, m := range it.Members {
		r.place(m, file)
	}
}

// renderDocs renders each item's comment, and its members', as written
// into file.
func (r *renderer) renderDocs(items []Item, file string) {
	for i := range items {
		it := &items[i]
		it.Doc = it.b.markdown(it.text, declHeadingLevel, file)
		r.renderDocs(it.Members, file)
	}
}

// noHeadingID gives a comment's heading no id: the same heading text
// recurs across declarations.
func noHeadingID(*comment.Heading) string { return "" }

// byCategory is the package's items by category: one category for a
// package that names one, its file's for each root declaration.
func (b *builder) byCategory() (map[string][]Item, error) {
	out := map[string][]Item{}
	add := func(pos token.Pos, it Item) error {
		cat := b.Category
		if cat == "" {
			file := filepath.Base(b.fset.Position(pos).Filename)
			c, ok := rootFiles[file]
			if !ok {
				return fmt.Errorf("apidoc: %s declares %s and is in no category: place it in rootFiles", file, it.Heading)
			}
			cat = c
		}
		out[cat] = append(out[cat], it)
		return nil
	}
	for _, v := range append(append([]*doc.Value{}, b.doc.Consts...), b.doc.Vars...) {
		if err := add(v.Decl.Pos(), b.value(v)); err != nil {
			return nil, err
		}
	}
	for _, f := range b.doc.Funcs {
		if err := add(f.Decl.Pos(), b.function(f, "")); err != nil {
			return nil, err
		}
	}
	for _, t := range b.doc.Types {
		if err := add(t.Decl.Pos(), b.typ(t)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// typ is a type with its constants, variables, constructors and methods.
func (b *builder) typ(t *doc.Type) Item {
	it := b.item([]string{t.Name}, t.Decl, t.Doc)
	for _, v := range append(append([]*doc.Value{}, t.Consts...), t.Vars...) {
		it.Members = append(it.Members, b.value(v))
	}
	for _, f := range t.Funcs {
		it.Members = append(it.Members, b.function(f, ""))
	}
	for _, m := range t.Methods {
		it.Members = append(it.Members, b.function(m, t.Name))
	}
	return it
}

// value is a constant or variable group, named by every name it declares.
func (b *builder) value(v *doc.Value) Item {
	return b.item(v.Names, v.Decl, v.Doc)
}

// function is a function, or a method of recv.
func (b *builder) function(f *doc.Func, recv string) Item {
	name := f.Name
	if recv != "" {
		name = recv + "." + name
	}
	return b.item([]string{name}, f.Decl, f.Doc)
}

// item is the declaration node, of the names given, with its comment.
func (b *builder) item(names []string, node ast.Node, text string) Item {
	anchors := make([]string, len(names))
	for i, n := range names {
		anchors[i] = b.doc.Name + "." + n
	}
	file := filepath.Base(b.fset.Position(node.Pos()).Filename)
	src := path.Join(b.Dir, file)
	return Item{
		Anchors:   anchors,
		Heading:   strings.Join(anchors, ", "),
		Decl:      b.source(node, b.files[file]),
		Source:    src,
		SourceURL: sourcePrefix + src,
		text:      text,
		b:         b,
	}
}

// source prints node with the comments inside it — a struct's fields', an
// interface's methods' — as go doc does.
func (b *builder) source(node ast.Node, file *ast.File) string {
	var buf bytes.Buffer
	cfg := printer.Config{Mode: printer.UseSpaces | printer.TabIndent, Tabwidth: 8}
	if err := cfg.Fprint(&buf, b.fset, &printer.CommentedNode{Node: node, Comments: file.Comments}); err != nil {
		return fmt.Sprintf("// apidoc: %v", err)
	}
	return buf.String()
}

// markdown is a doc comment as Markdown, its headings at level, its doc
// links as written into file.
func (b *builder) markdown(text string, level int, file string) string {
	if text == "" {
		return ""
	}
	p := comment.Printer{
		HeadingLevel: level,
		HeadingID:    noHeadingID,
		DocLinkURL:   func(link *comment.DocLink) string { return b.linkURL(link, file) },
	}
	return strings.TrimSpace(string(p.Markdown(b.doc.Parser().Parse(text))))
}

// linkURL points a doc link, written into file, at the declaration's
// anchor when the reference holds it, at pkg.go.dev otherwise.
func (b *builder) linkURL(link *comment.DocLink, file string) string {
	importPath := link.ImportPath
	if importPath == "" {
		importPath = b.importPath
	}
	name, ours := b.r.names[importPath]
	if !ours {
		return link.DefaultURL(pkgGoDev)
	}
	id := packageAnchorPrefix + name
	switch {
	case link.Recv != "":
		id = name + "." + strings.TrimPrefix(link.Recv, "*") + "." + link.Name
	case link.Name != "":
		id = name + "." + link.Name
	}
	target, known := b.r.files[id]
	switch {
	case !known:
		return link.DefaultURL(pkgGoDev)
	case target == file:
		return "#" + id
	default:
		return target + "#" + id
	}
}
