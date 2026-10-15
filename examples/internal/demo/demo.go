// Package demo is what every example shares: a logger that writes each step
// to stdout, and a stop at the first step that does not do what the example
// says it does.
package demo

import (
	"errors"
	"log/slog"
	"os"
)

// exitFailed is the status an example exits with when a step fails.
const exitFailed = 1

// New returns a logger for the example named name. Each line is one step,
// as key=value pairs, without the time: the steps are what is worth reading.
func New(name string) *slog.Logger {
	h := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	})
	return slog.New(h).With("example", name)
}

// Must stops the example when err is not nil, naming the step that failed.
func Must(log *slog.Logger, err error, step string) {
	if err != nil {
		log.Error("step failed", "step", step, "err", err)
		os.Exit(exitFailed)
	}
}

// Refused logs that step was refused with want, as the example expects, and
// stops the example when it was not.
func Refused(log *slog.Logger, err, want error, step string) {
	if !errors.Is(err, want) {
		log.Error("step was not refused as expected", "step", step, "want", want, "got", err)
		os.Exit(exitFailed)
	}
	log.Info("refused, as expected", "step", step, "err", err)
}
