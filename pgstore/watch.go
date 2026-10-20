package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// RevocationChannel is the channel the schema notifies on every statement
// that writes a revocation (migrations/006_revocation_notify.sql spells it
// too, and a test holds the two to one name).
const RevocationChannel = "limes_revocations"

// How long the watcher waits before it connects again: from
// watchRetryFirst, doubling up to watchRetryMost, and from the first again
// once a connection listens.
const (
	watchRetryFirst = 100 * time.Millisecond
	watchRetryMost  = 10 * time.Second
)

// Clearer is a cache the watcher clears: limes.RevocationCache is.
type Clearer interface {
	Clear()
}

// RevocationWatcher clears revocation caches when any replica revokes: it
// listens on RevocationChannel and clears every cache on each notification,
// and after each connection it opens, since a revocation made while it was
// not listening sent a notification it never heard.
type RevocationWatcher struct {
	// Connect opens the connection the watcher listens on. It must be the
	// watcher's own, not one a pool takes back with the LISTEN still on
	// it: from a pool, Acquire and Hijack. Required.
	Connect func(ctx context.Context) (*pgx.Conn, error)
	// Caches are the caches cleared.
	Caches []Clearer
	// OnError, when set, hears each failure the watcher recovers from by
	// connecting again.
	OnError func(error)
}

// errNoConnect is a RevocationWatcher with no Connect.
var errNoConnect = errors.New("pgstore: RevocationWatcher.Connect required")

// Run watches until ctx ends, and returns its error.
func (w RevocationWatcher) Run(ctx context.Context) error {
	if w.Connect == nil {
		return errNoConnect
	}
	wait := watchRetryFirst
	for {
		listened, err := w.listen(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if w.OnError != nil {
			w.OnError(err)
		}
		if listened {
			wait = watchRetryFirst
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		wait = min(2*wait, watchRetryMost)
	}
}

// listen connects, listens and clears the caches on each notification
// until the connection fails, which it returns; listened reports whether
// it got as far as listening.
func (w RevocationWatcher) listen(ctx context.Context) (listened bool, err error) {
	conn, err := w.Connect(ctx)
	if err != nil {
		return false, fmt.Errorf("pgstore: watch revocations: connect: %w", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{RevocationChannel}.Sanitize()); err != nil {
		return false, fmt.Errorf("pgstore: watch revocations: listen: %w", err)
	}
	// Listening now, so whatever was revoked before is in the store: what
	// the caches hold from before may be stale.
	w.clear()
	for {
		if _, err := conn.WaitForNotification(ctx); err != nil {
			return true, fmt.Errorf("pgstore: watch revocations: %w", err)
		}
		w.clear()
	}
}

func (w RevocationWatcher) clear() {
	for _, c := range w.Caches {
		c.Clear()
	}
}
