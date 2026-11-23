package pgstore

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Each record's level and message, in the order logged.
type logLine struct {
	level   slog.Level
	message string
}

// lines is what logged holds, level and message.
func (h *logRecords) lines() []logLine {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]logLine, 0, len(h.records))
	for _, r := range h.records {
		out = append(out, logLine{r.Level, r.Message})
	}
	return out
}

// withMessage is the records logged with message, in order.
func (h *logRecords) withMessage(message string) []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []slog.Record
	for _, r := range h.records {
		if r.Message == message {
			out = append(out, r)
		}
	}
	return out
}

// attrOf is the value of key on r, and whether r has it.
func attrOf(r slog.Record, key string) (slog.Value, bool) {
	var v slog.Value
	found := false
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			v, found = a.Value, true
			return false
		}
		return true
	})
	return v, found
}

// wantFailedAttempts checks r counts failed attempts as want.
func wantFailedAttempts(t *testing.T, r slog.Record, want int64) {
	t.Helper()
	v, ok := attrOf(r, logKeyFailedAttempts)
	if !ok || v.Kind() != slog.KindInt64 || v.Int64() != want {
		t.Errorf("%q %s = %v (present %v), want %d", r.Message, logKeyFailedAttempts, v, ok, want)
	}
}

// The watcher logs that it starts, that it listens, that it lost the
// connection and when it tries again, that it listens again after the
// failure, and that it stopped: a consumer's logs tell a recovered watch
// from one still failing.
func TestRevocationWatcherLogsItsLife(t *testing.T) {
	newTenantFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cleared := make(clearCounter)
	logged := &logRecords{}
	done := make(chan error, 1)
	go func() {
		done <- RevocationWatcher{Connect: connectApp, Caches: []Clearer{cleared}, Logger: slog.New(logged)}.Run(ctx)
	}()
	awaitClear(t, cleared, "once listening")
	if _, err := owner.Exec(t.Context(), sqlEndWatcher, watcherName); err != nil {
		t.Fatal(err)
	}
	awaitClear(t, cleared, "after connecting again")
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run after its context ended = %v, want context.Canceled", err)
	}

	lines := logged.lines()
	want := []logLine{
		{slog.LevelInfo, logWatchStarting},
		{slog.LevelInfo, logWatchListening},
		{slog.LevelWarn, logWatchFailed},
		{slog.LevelInfo, logWatchListening},
		{slog.LevelInfo, logWatchStopped},
	}
	if len(lines) != len(want) {
		t.Fatalf("logged %v, want %v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %v, want %v", i, lines[i], want[i])
		}
	}
	listening := logged.withMessage(logWatchListening)
	wantFailedAttempts(t, listening[0], 0)
	wantFailedAttempts(t, listening[1], 1)
	failed := logged.withMessage(logWatchFailed)[0]
	wantFailedAttempts(t, failed, 1)
	if v, ok := attrOf(failed, logKeyError); !ok || v.Any() == nil {
		t.Errorf("%q carries no %s", logWatchFailed, logKeyError)
	}
	if v, ok := attrOf(failed, logKeyRetryAfter); !ok || v.Kind() != slog.KindDuration || v.Duration() != watchRetryFirst {
		t.Errorf("%q %s = %v (present %v), want %s", logWatchFailed, logKeyRetryAfter, v, ok, watchRetryFirst)
	}
	if v, ok := attrOf(logged.withMessage(logWatchStarting)[0], logKeyChannel); !ok || v.String() != RevocationChannel {
		t.Errorf("%q %s = %v (present %v), want %s", logWatchStarting, logKeyChannel, v, ok, RevocationChannel)
	}
}

// watchFailuresSeen is how many failed connections the next check waits
// for: enough to see the count and the wait grow.
const watchFailuresSeen = 3

// A watcher that cannot connect never says it listens: each failure is a
// warning counting the attempts since it last listened, with the wait
// before the next, doubling.
func TestRevocationWatcherLogsEachFailedConnect(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	refused := errors.New("connection refused")
	failures := make(chan struct{}, watchFailuresSeen)
	logged := &logRecords{}
	done := make(chan error, 1)
	go func() {
		done <- RevocationWatcher{
			Connect: func(context.Context) (*pgx.Conn, error) { return nil, refused },
			Logger:  slog.New(logged),
			OnError: func(error) {
				select {
				case failures <- struct{}{}:
				default:
				}
			},
		}.Run(ctx)
	}()
	for range watchFailuresSeen {
		select {
		case <-failures:
		case <-time.After(watchWait):
			t.Fatalf("fewer than %d failures within %s", watchFailuresSeen, watchWait)
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run after its context ended = %v, want context.Canceled", err)
	}

	if got := logged.withMessage(logWatchListening); len(got) != 0 {
		t.Errorf("logged %q %d times, never listening", logWatchListening, len(got))
	}
	failed := logged.withMessage(logWatchFailed)
	if len(failed) < watchFailuresSeen {
		t.Fatalf("logged %q %d times, want at least %d", logWatchFailed, len(failed), watchFailuresSeen)
	}
	wait := watchRetryFirst
	for i, r := range failed[:watchFailuresSeen] {
		if r.Level != slog.LevelWarn {
			t.Errorf("failure %d at %s, want %s", i, r.Level, slog.LevelWarn)
		}
		wantFailedAttempts(t, r, int64(i+1))
		if v, _ := attrOf(r, logKeyError); !errors.Is(v.Any().(error), refused) {
			t.Errorf("failure %d %s = %v, want it to wrap %v", i, logKeyError, v, refused)
		}
		if v, _ := attrOf(r, logKeyRetryAfter); v.Duration() != wait {
			t.Errorf("failure %d %s = %v, want %s", i, logKeyRetryAfter, v, wait)
		}
		wait = min(2*wait, watchRetryMost)
	}
}

// A watcher refused for its configuration says why before it returns.
func TestRevocationWatcherLogsARefusedConfig(t *testing.T) {
	connect := func(context.Context) (*pgx.Conn, error) { return nil, errors.New("unused") }
	for _, tc := range []struct {
		name    string
		watcher RevocationWatcher
		want    error
	}{
		{"no Connect", RevocationWatcher{}, errNoConnect},
		{"negative Heartbeat", RevocationWatcher{Connect: connect, Heartbeat: -time.Second}, errNegativeHeartbeat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logged := &logRecords{}
			tc.watcher.Logger = slog.New(logged)
			if err := tc.watcher.Run(t.Context()); !errors.Is(err, tc.want) {
				t.Fatalf("Run = %v, want %v", err, tc.want)
			}
			got := logged.withMessage(logWatchRefused)
			if len(got) != 1 || got[0].Level != slog.LevelError {
				t.Fatalf("logged %v, want one %q at %s", logged.lines(), logWatchRefused, slog.LevelError)
			}
			if v, _ := attrOf(got[0], logKeyError); !errors.Is(v.Any().(error), tc.want) {
				t.Errorf("%s = %v, want %v", logKeyError, v, tc.want)
			}
		})
	}
}

// With no Logger, the watcher logs to slog.Default().
func TestRevocationWatcherLogsToTheDefaultLogger(t *testing.T) {
	if got := (RevocationWatcher{}).logger(); got != slog.Default() {
		t.Errorf("logger() = %v, want slog.Default()", got)
	}
	mine := slog.New(&logRecords{})
	if got := (RevocationWatcher{Logger: mine}).logger(); got != mine {
		t.Errorf("logger() = %v, want the Logger set", got)
	}
}
