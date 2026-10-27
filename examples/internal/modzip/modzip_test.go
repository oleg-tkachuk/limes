// Package modzip checks what `go get` downloads of limes: the module zip,
// built from the repository's last commit as the module proxy builds it from
// a tag.
package modzip

import (
	"archive/zip"
	"bytes"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"
)

const (
	// repoRoot is the repository, from this package's directory.
	repoRoot = "../../.."
	// modulePath is limes's module path; zipVersion is any version it may
	// carry, which names the directory the zip's files sit under.
	modulePath = "github.com/oleg-tkachuk/limes"
	zipVersion = "v0.0.0"
	// revision is the commit the zip is built from.
	revision = "HEAD"
	// ciDir holds the CI scripts and workflows; goMod names a module's file.
	ciDir = ".github"
	goMod = "go.mod"
)

// mustHave are files a consumer of the module needs: its go.mod, its
// licence, and the package's own source.
var mustHave = []string{goMod, "LICENSE", "NOTICE", "doc.go", "verifier.go"}

// zipFiles is the module zip of revision, as the paths of its files relative
// to the module. The tree is the commit's, through git archive, as the go
// command reads a repository; modzip.CreateFromVCS would do the same but
// finds no repository in a git worktree, whose .git is a file.
func zipFiles(t *testing.T, root string) map[string]bool {
	t.Helper()
	tree := t.TempDir()
	archive := exec.CommandContext(t.Context(), "git", "-C", root, "archive", "--format=tar", revision)
	extract := exec.CommandContext(t.Context(), "tar", "-x", "-C", tree)
	pipe, err := archive.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	extract.Stdin = pipe
	if err := archive.Start(); err != nil {
		t.Fatal(err)
	}
	if err := extract.Run(); err != nil {
		t.Fatalf("tar: %v", err)
	}
	if err := archive.Wait(); err != nil {
		t.Fatalf("git archive: %v", err)
	}
	var buf bytes.Buffer
	if err := modzip.CreateFromDir(&buf, module.Version{Path: modulePath, Version: zipVersion}, tree); err != nil {
		t.Fatalf("building the module zip: %v", err)
	}
	r, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	prefix := modulePath + "@" + zipVersion + "/"
	files := map[string]bool{}
	for _, f := range r.File {
		name, ok := strings.CutPrefix(f.Name, prefix)
		if !ok {
			t.Fatalf("zip entry %s is outside %s", f.Name, prefix)
		}
		files[name] = true
	}
	return files
}

// nestedModules is the directory of every module the repository tracks
// besides limes itself, .github's among them.
func nestedModules(t *testing.T, root string) []string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", "-C", root, "ls-files", "--", "*/"+goMod).Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	var dirs []string
	for line := range strings.Lines(string(out)) {
		dirs = append(dirs, path.Dir(strings.TrimSpace(line)))
	}
	return dirs
}

// The zip holds the module and nothing of the repository around it: neither
// the CI scripts nor any nested module, each of which is a download of its
// own.
func TestModuleZipHoldsTheModuleAlone(t *testing.T) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	files := zipFiles(t, root)
	for _, name := range mustHave {
		if !files[name] {
			t.Errorf("the module zip lacks %s", name)
		}
	}
	excluded := nestedModules(t, root)
	if !strings.Contains(strings.Join(excluded, "\n"), ciDir) {
		t.Fatalf("%s has no %s of its own: nested modules are %q", ciDir, goMod, excluded)
	}
	for name := range files {
		for _, dir := range excluded {
			if strings.HasPrefix(name, dir+"/") {
				t.Errorf("the module zip holds %s, of the module in %s", name, dir)
			}
		}
	}
}
