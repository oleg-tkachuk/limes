package demo

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// captureStdout runs f and returns what it wrote to stdout.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = stdout }()
	f()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// A step logs to stdout as one line, naming its example, without the time.
func TestNewLogsEachStepToStdout(t *testing.T) {
	out := captureStdout(t, func() { New("quickstart").Info("capability issued", "id", 7) })
	for _, want := range []string{`msg="capability issued"`, "example=quickstart", "id=7"} {
		if !strings.Contains(out, want) {
			t.Errorf("line %q lacks %s", out, want)
		}
	}
	if strings.Contains(out, "time=") || strings.Count(out, "\n") != 1 {
		t.Errorf("line %q: want one line without the time", out)
	}
}

// A refusal the example expects is logged with its step and error, and
// the example goes on. The other paths of Must and Refused exit the
// process, which a test in the same process cannot observe; running the
// examples checks them.
func TestRefusedLogsAnExpectedRefusal(t *testing.T) {
	want := errors.New("limes: revoked")
	out := captureStdout(t, func() {
		log := New("quickstart")
		Must(log, nil, "issue")
		Refused(log, want, want, "verify the revoked token")
	})
	for _, s := range []string{`msg="refused, as expected"`, `step="verify the revoked token"`, `err="limes: revoked"`} {
		if !strings.Contains(out, s) {
			t.Errorf("output %q lacks %s", out, s)
		}
	}
}
