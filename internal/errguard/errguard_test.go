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

const doublingSample = `package p

import "fmt"

func context(err error) error { return fmt.Errorf("limes: sign: %w", err) }

func family(err error) error { return fmt.Errorf("%w: %w", ErrRoot, err) }

func detail(err error) error { return fmt.Errorf("%w: ctx", err) }

func other(err error) error { return fmt.Errorf("dpop: key: %w", err) }

func plain(n int) error { return fmt.Errorf("limes: n %d", n) }

func quoted(err error) error { return fmt.Errorf("%w: %s", ErrRoot, err.Error()) }
`

func TestDoubling(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.go")
	if err := os.WriteFile(path, []byte(doublingSample), 0o600); err != nil {
		t.Fatal(err)
	}
	sites, err := Doubling([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range sites {
		got = append(got, s.Func)
	}
	if want := []string{"context", "family", "quoted"}; !slices.Equal(got, want) {
		t.Errorf("Doubling found %q, want %q", got, want)
	}
}
