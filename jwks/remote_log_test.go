package jwks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// Log messages a fetch writes, as the tests look for them.
const (
	msgFetched    = "jwks: key set fetched"
	msgSkipped    = "jwks: fetch skipped: the last attempt is within MinRefreshInterval"
	msgKeptStale  = "jwks: key set fetch failed; serving the held set until it is MaxStale old"
	msgFailClosed = "jwks: key set fetch failed with no set to serve; refusing tokens until a fetch succeeds"
)

// Steps of the fixture's clock: past RefreshInterval within MaxStale, past
// MinRefreshInterval, and past MaxStale.
const (
	pastRefresh  = 2 * time.Minute
	pastMinFetch = 11 * time.Second
	pastMaxStale = time.Hour
)

// debugLog is a JSON logger at DEBUG writing to the buffer it returns.
func debugLog() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}

// takeLines parses and empties what buf holds, one JSON log line each.
func takeLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for line := range strings.Lines(buf.String()) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		lines = append(lines, m)
	}
	buf.Reset()
	return lines
}

// lineWith is the line logged with msg, or fails the test.
func lineWith(t *testing.T, lines []map[string]any, msg string) map[string]any {
	t.Helper()
	for _, l := range lines {
		if l[slog.MessageKey] == msg {
			return l
		}
	}
	t.Fatalf("no line %q among %v", msg, lines)
	return nil
}

// Each background fetch is logged with the endpoint: a set fetched at DEBUG
// with its key count, a fetch skipped inside MinRefreshInterval at DEBUG, a
// failure at WARN while the held set is still served, with the error and the
// set's age, and at ERROR once the set is past MaxStale.
func TestRemoteLogsItsFetches(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, r, clock, kid, _ := newRemoteFixture(t)
		logger, buf := debugLog()
		r.cfg.Logger = logger
		ctx := context.Background()

		if _, err := r.PublicKey(ctx, kid); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		fetched := lineWith(t, takeLines(t, buf), msgFetched)
		if fetched[slog.LevelKey] != slog.LevelDebug.String() || fetched[logKeyEndpoint] != fixtureURL ||
			fetched[logKeyKeyCount] != float64(len(srv.keys)) || fetched[logKeyNotModified] != false {
			t.Errorf("fetch logged as %v", fetched)
		}

		if _, err := r.PublicKey(ctx, "made-up"); !errors.Is(err, ErrUnknownKID) {
			t.Fatalf("unknown kid: %v", err)
		}
		skipped := lineWith(t, takeLines(t, buf), msgSkipped)
		if skipped[slog.LevelKey] != slog.LevelDebug.String() || skipped[logKeyEndpoint] != fixtureURL {
			t.Errorf("skip logged as %v", skipped)
		}

		srv.set(func() { srv.fail = true })
		clock.Advance(pastRefresh)
		if _, err := r.PublicKey(ctx, kid); err != nil {
			t.Fatalf("within MaxStale: %v", err)
		}
		synctest.Wait()
		kept := lineWith(t, takeLines(t, buf), msgKeptStale)
		if kept[slog.LevelKey] != slog.LevelWarn.String() || kept[logKeyEndpoint] != fixtureURL ||
			kept[logKeyError] == nil || kept[logKeyHeldAge] != float64(pastRefresh) || kept[logKeyRetryAfter] == nil {
			t.Errorf("a failed fetch with a held set logged as %v", kept)
		}

		clock.Advance(pastMaxStale)
		if _, err := r.PublicKey(ctx, kid); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("past MaxStale: %v, want ErrUnavailable", err)
		}
		closed := lineWith(t, takeLines(t, buf), msgFailClosed)
		if closed[slog.LevelKey] != slog.LevelError.String() || closed[logKeyEndpoint] != fixtureURL || closed[logKeyError] == nil {
			t.Errorf("a failed fetch with no set logged as %v", closed)
		}

		srv.set(func() { srv.fail = false })
		clock.Advance(pastMinFetch)
		if _, err := r.PublicKey(ctx, kid); err != nil {
			t.Fatalf("after recovery: %v", err)
		}
		confirmed := lineWith(t, takeLines(t, buf), msgFetched)
		if confirmed[logKeyNotModified] != true || confirmed[logKeyKeyCount] != float64(len(srv.keys)) {
			t.Errorf("a set confirmed unchanged logged as %v", confirmed)
		}
	})
}
