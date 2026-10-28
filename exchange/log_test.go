package exchange

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/oleg-tkachuk/limes"
)

// records is a slog.Handler keeping what it is given.
type records struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (r *records) Enabled(context.Context, slog.Level) bool { return true }
func (r *records) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recs = append(r.recs, rec)
	return nil
}
func (r *records) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *records) WithGroup(string) slog.Handler      { return r }

// last is the level and message of the last record.
func (r *records) last() (slog.Level, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.recs) == 0 {
		return 0, ""
	}
	rec := r.recs[len(r.recs)-1]
	return rec.Level, rec.Message
}

// lastWraps reports whether the last record's error attribute is or wraps
// target.
func (r *records) lastWraps(target error) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.recs) == 0 {
		return false
	}
	found := false
	r.recs[len(r.recs)-1].Attrs(func(a slog.Attr) bool {
		err, ok := a.Value.Any().(error)
		found = found || ok && errors.Is(err, target)
		return true
	})
	return found
}

// Every exchange is logged: a grant at INFO, a refusal at WARN, the
// server's failure at ERROR.
func TestExchangeLogsEveryOutcome(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	logs := &records{}
	f.ex.cfg.Logger = slog.New(logs)
	token := f.idp.sign(t, TypeAccessToken, accessClaims())
	failing := func(context.Context, Grant) (limes.IssueRequest, error) {
		return limes.IssueRequest{}, errors.New("down")
	}
	for name, tc := range map[string]struct {
		policy Policy
		req    Request
		level  slog.Level
		msg    string
	}{
		"a grant":   {f.readPolicy, request(token), slog.LevelInfo, "exchange granted"},
		"a refusal": {f.readPolicy, request("a.b.c"), slog.LevelWarn, "exchange refused"},
		"a failure": {failing, request(token), slog.LevelError, "exchange refused"},
	} {
		f.ex.cfg.Policy = tc.policy
		_, _ = f.ex.Exchange(t.Context(), tc.req)
		if level, msg := logs.last(); level != tc.level || msg != tc.msg {
			t.Errorf("%s logged %s %q, want %s %q", name, level, msg, tc.level, tc.msg)
		}
	}
}
