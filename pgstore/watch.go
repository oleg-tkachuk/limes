package pgstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
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

// MaxNotifiedRevocations is the most capability ids one notification names
// (migrations/018_revocation_lineage.sql spells it too, and a test holds
// the two to one number): past it, the notification names none, and a cache
// is cleared whole.
const MaxNotifiedRevocations = 100

// Clearer is a cache the watcher clears: limes.RevocationCache is.
type Clearer interface {
	Clear()
}

// RevokedClearer is a cache that can drop only what a revocation of some
// capabilities outdates: limes.RevocationCache is.
type RevokedClearer interface {
	Clearer
	ClearRevoked(ids ...uuid.UUID)
}

// RevocationWatcher clears revocation caches when any replica revokes: it
// listens on RevocationChannel and, on each notification, drops from each
// cache what the revocation outdates — with ClearRevoked, from a
// RevokedClearer, when the notification names the capabilities revoked —
// or clears it whole: a cache that is no RevokedClearer, a notification
// that names none (a Biscuit copy's revocation, a statement revoking more
// than MaxNotifiedRevocations capabilities that no ancestor's id covers),
// and after each connection it opens, since a revocation made while it was
// not listening sent a notification it never heard. Between connections, a
// cache's TTL bounds how long a revocation may go unseen.
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
	w.clear(nil)
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return true, fmt.Errorf("pgstore: watch revocations: %w", err)
		}
		w.clear(revokedIDs(n.Payload))
	}
}

// revokedIDs is the capability ids a notification's payload names, nil for
// one that names none or that does not parse.
func revokedIDs(payload string) []uuid.UUID {
	fields := strings.Fields(payload)
	ids := make([]uuid.UUID, 0, len(fields))
	for _, f := range fields {
		id, err := uuid.Parse(f)
		if err != nil {
			return nil
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	return ids
}

// clear drops what a revocation of ids outdates from each cache that can
// tell, and clears the others and every cache when ids is nil.
func (w RevocationWatcher) clear(ids []uuid.UUID) {
	for _, c := range w.Caches {
		if r, ok := c.(RevokedClearer); ok && ids != nil {
			r.ClearRevoked(ids...)
			continue
		}
		c.Clear()
	}
}
