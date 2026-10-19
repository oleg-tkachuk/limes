// Package readme checks the Go in the repository's README against the
// module it describes.
package readme

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	// readmePath is the README, from this package's directory.
	readmePath = "../../../README.md"
	// fiveMinutes heads the README's walkthrough, whose first Go block is
	// the snippet run here.
	fiveMinutes = "## Five minutes"
	goFence     = "```go\n"
	endFence    = "```"
	// snippetDir is where the snippet is compiled as a program of this
	// module, a directory that exists only in the build's overlay.
	snippetDir = "snippet"
	// ranToTheEnd is what the program prints once every step of the
	// snippet passed.
	ranToTheEnd = "five minutes: ok"
)

// program wraps the snippet, the body of a function returning an error, in
// a main package. The imports are the packages the snippet uses; one it
// starts using must be added here, and one it stops using fails the build.
const program = `package main

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/jwks"
	"github.com/oleg-tkachuk/limes/memstore"
	"github.com/oleg-tkachuk/limes/meter"
)

func run() error {
%s	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	fmt.Println(%q)
}
`

// The README's five-minute walkthrough builds against this commit's module
// and runs to the end: a snippet that no longer compiles, or a step that
// refuses what it says it allows, fails here.
func TestFiveMinutesRuns(t *testing.T) {
	readme, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(readme)
	at := strings.Index(text, fiveMinutes)
	if at < 0 {
		t.Fatalf("README has no %q section", fiveMinutes)
	}
	start := strings.Index(text[at:], goFence)
	if start < 0 {
		t.Fatalf("README's %q section has no Go block", fiveMinutes)
	}
	body := text[at+start+len(goFence):]
	end := strings.Index(body, endFence)
	if end < 0 {
		t.Fatal("README's Go block is not closed")
	}
	src := fmt.Sprintf(program, body[:end], ranToTheEnd)

	dir := t.TempDir()
	file := filepath.Join(dir, "main.go")
	if err := os.WriteFile(file, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(wd, snippetDir, "main.go")
	overlay, err := json.Marshal(map[string]map[string]string{"Replace": {target: file}})
	if err != nil {
		t.Fatal(err)
	}
	overlayFile := filepath.Join(dir, "overlay.json")
	if err := os.WriteFile(overlayFile, overlay, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(t.Context(), "go", "run", "-overlay", overlayFile, "./"+snippetDir).CombinedOutput()
	if err != nil {
		t.Fatalf("the README's snippet: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), ranToTheEnd) {
		t.Errorf("the README's snippet did not run to the end:\n%s", out)
	}
}
