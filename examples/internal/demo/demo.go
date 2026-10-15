// Package demo is what every example shares: a logger that writes each step
// to stdout for a person to read, and a stop at the first step that does not
// do what the example says it does.
//
// A step is one line, marked by what happened — markStep for a step done,
// markExpected for a refusal the example sets out to show, markFailed for a
// step that went wrong — and its details follow on lines of their own,
// indented, each a name and a value, the names aligned.
package demo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// exitFailed is the status an example exits with when a step fails.
const exitFailed = 1

// levelExpected is the level of a refusal the example sets out to show: it
// reads as a success, not as an error.
const levelExpected = slog.LevelInfo + 1

// The mark at the start of each step's line.
const (
	markStep     = "•"
	markExpected = "✓"
	markFailed   = "✗"
)

// detailIndent is how far a step's details sit below its line; detailGap
// separates the longest name from its value.
const (
	detailIndent = "    "
	detailGap    = 2
)

// errorKey is the name a step's error is shown under.
const errorKey = "error"

// New returns a logger for the example named name, and writes its title.
func New(name string) *slog.Logger {
	return newLogger(os.Stdout, name)
}

func newLogger(w io.Writer, name string) *slog.Logger {
	_, _ = fmt.Fprintf(w, "%s\n%s\n", name, strings.Repeat("─", len([]rune(name))))
	return slog.New(&handler{w: w, mu: &sync.Mutex{}})
}

// Must stops the example when err is not nil, naming the step that failed.
func Must(log *slog.Logger, err error, step string) {
	if err != nil {
		log.Error("step failed: "+step, errorKey, err)
		os.Exit(exitFailed)
	}
}

// Refused logs that step was refused with want, as the example expects, and
// stops the example when it was not.
func Refused(log *slog.Logger, err, want error, step string) {
	if !errors.Is(err, want) {
		log.Error("not refused as expected: "+step, "want", want, "got", err)
		os.Exit(exitFailed)
	}
	log.Log(context.Background(), levelExpected, "refused, as expected: "+step, errorKey, err)
}

// handler writes each record as a marked line and its attributes below it.
type handler struct {
	w      io.Writer
	mu     *sync.Mutex
	attrs  []slog.Attr
	prefix string
}

func (h *handler) Enabled(_ context.Context, l slog.Level) bool { return l >= slog.LevelInfo }

func (h *handler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(mark(r.Level))
	b.WriteString(" ")
	b.WriteString(r.Message)
	b.WriteString("\n")

	attrs := append([]slog.Attr(nil), h.attrs...)
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, slog.Attr{Key: h.prefix + a.Key, Value: a.Value})
		return true
	})
	width := 0
	for _, a := range attrs {
		width = max(width, len([]rune(a.Key)))
	}
	for _, a := range attrs {
		fmt.Fprintf(&b, "%s%-*s%s\n", detailIndent, width+detailGap, a.Key, a.Value.Resolve().String())
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *h
	c.attrs = append([]slog.Attr(nil), h.attrs...)
	for _, a := range attrs {
		c.attrs = append(c.attrs, slog.Attr{Key: h.prefix + a.Key, Value: a.Value})
	}
	return &c
}

func (h *handler) WithGroup(name string) slog.Handler {
	c := *h
	c.prefix = h.prefix + name + "."
	return &c
}

// mark is the mark of a step logged at l.
func mark(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return markFailed
	case l == levelExpected:
		return markExpected
	}
	return markStep
}
