package demo

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
)

// A step reads as its mark and message, then its details, one per line,
// indented, their names aligned: no level, no time, no key=value noise.
func TestAStepReadsAsLines(t *testing.T) {
	var out bytes.Buffer
	log := newLogger(&out, "quickstart")
	log.Info("capability issued", "id", 7, "token size", "746 bytes")
	log.With("tenant", "t1").WithGroup("budget").Info("budget set", "max", 25)
	log.Error("step failed: issue", errorKey, errors.New("boom"))

	want := "quickstart\n" +
		"──────────\n" +
		"• capability issued\n" +
		"    id          7\n" +
		"    token size  746 bytes\n" +
		"• budget set\n" +
		"    tenant      t1\n" +
		"    budget.max  25\n" +
		"✗ step failed: issue\n" +
		"    error  boom\n"
	if got := out.String(); got != want {
		t.Errorf("output:\n%s\nwant:\n%s", got, want)
	}
}

// Below Info nothing is written.
func TestDebugIsNotShown(t *testing.T) {
	var out bytes.Buffer
	log := newLogger(&out, "x")
	out.Reset()
	log.Debug("internal detail")
	if out.Len() != 0 {
		t.Errorf("debug wrote %q", out.String())
	}
}

// A refusal the example expects is marked as a success, with its step and
// error, and the example goes on. The other paths of Must and Refused exit
// the process, which a test in the same process cannot observe; running the
// examples checks them.
func TestRefusedMarksAnExpectedRefusal(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	want := errors.New("limes: revoked")
	log := New("quickstart")
	Must(log, nil, "issue")
	Refused(log, want, want, "verify the revoked token")
	os.Stdout = stdout
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	wantOut := "quickstart\n──────────\n" +
		"✓ refused, as expected: verify the revoked token\n" +
		"    error  limes: revoked\n"
	if string(out) != wantOut {
		t.Errorf("output:\n%s\nwant:\n%s", out, wantOut)
	}
}

// The handler is an slog.Handler that keeps to slog's own rules.
var _ slog.Handler = (*handler)(nil)
