package errguard

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

const sample = `package p

import (
	"errors"
	"fmt"
)

var ErrRoot = errors.New("p: root")

var (
	ErrOther = errors.New("p: other")
	errHidden = errors.New("p: hidden")
)

var errWrapped = fmt.Errorf("%w: p: wrapped", ErrRoot)

func bare() error { return errors.New("bare") }

func bareFormat(n int) error { return fmt.Errorf("n %d", n) }

func wrapped(err error) error { return fmt.Errorf("%w: ctx", err) }

func concatenated(format string) error { return fmt.Errorf("%w: "+format, ErrRoot) }
`

func TestBare(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.go")
	if err := os.WriteFile(path, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}
	sites, err := Bare([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range sites {
		got = append(got, s.Func)
	}
	if want := []string{"errHidden", "bare", "bareFormat"}; !slices.Equal(got, want) {
		t.Errorf("Bare found %q, want %q", got, want)
	}
}
