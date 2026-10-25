package pgstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/oleg-tkachuk/limes"
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
	// DefaultWatchHeartbeat is how long the watcher waits on a quiet
	// connection before it checks the connection is alive
	// (RevocationWatcher.Heartbeat).
	DefaultWatchHeartbeat = 30 * time.Second
)

// MaxNotifiedRevocations is the most capability ids one notification names
// (migrations/021_notify_unseen_revocations.sql spells it too, and a test holds
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
// not listening sent a notification it never heard. A cache that is a
// limes.RevocationSignalTarget is told instead, through a
// limes.RevocationSignal Run opens on it and closes when it returns —
// Listening once it listens, Revoked or RevokedAll on each notification,
// and NotListening when the connection is lost — so it serves its TTL
// alone while no notification can reach it. Between connections, that TTL
// bounds how long a revocation may go unseen.
//
// Connect reaches the primary: PostgreSQL notifies only the sessions of the
// server a revocation committed on, and a hot standby refuses LISTEN, so a
// watcher pointed at one never listens and its caches stay on their TTL.
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
	// Heartbeat is how long the watcher waits for a notification before it
	// pings the connection, with as long again for the answer: a ping that
	// fails or does not come back ends the connection as a lost one does,
	// and a signalled cache is told NotListening. A connection that died without a
	// word — a host gone, a network cut — would otherwise go on vouching
	// for the caches until TCP gave up on it, minutes later, while they
	// serve their SyncedTTL; with it, for at most twice Heartbeat. Zero:
	// DefaultWatchHeartbeat. Negative is refused.
	Heartbeat time.Duration
}

// errNegativeHeartbeat is a RevocationWatcher with a negative Heartbeat.
var errNegativeHeartbeat = errors.New("pgstore: RevocationWatcher.Heartbeat is negative")

// heartbeat is the Heartbeat in force.
func (w RevocationWatcher) heartbeat() time.Duration {
	if w.Heartbeat == 0 {
		return DefaultWatchHeartbeat
	}
	return w.Heartbeat
}

// errNoConnect is a RevocationWatcher with no Connect.
var errNoConnect = errors.New("pgstore: RevocationWatcher.Connect required")

// Run watches until ctx ends, and returns its error.
func (w RevocationWatcher) Run(ctx context.Context) error {
	if w.Connect == nil {
		return errNoConnect
	}
	if w.Heartbeat < 0 {
		return errNegativeHeartbeat
	}
	signals := w.openSignals()
	defer closeSignals(signals)
	wait := watchRetryFirst
	for {
		listened, err := w.listen(ctx, signals)
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
func (w RevocationWatcher) listen(ctx context.Context, signals []*limes.RevocationSignal) (listened bool, err error) {
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
	w.listening(signals)
	defer notListening(signals)
	beat := w.heartbeat()
	for {
		n, err := waitForNotification(ctx, conn, beat)
		switch {
		case errors.Is(err, errQuiet):
			if err := ping(ctx, conn, beat); err != nil {
				return true, fmt.Errorf("pgstore: watch revocations: heartbeat: %w", err)
			}
		case err != nil:
			return true, fmt.Errorf("pgstore: watch revocations: %w", err)
		default:
			w.signal(signals, revokedIDs(n.Payload))
		}
	}
}

// errQuiet is a wait for a notification that heard none within its bound.
var errQuiet = errors.New("pgstore: no notification within the heartbeat")

// waitForNotification waits on conn for a notification for at most beat;
// errQuiet when none came. pgx ends a wait by a deadline on the socket,
// which leaves the connection usable.
func waitForNotification(ctx context.Context, conn *pgx.Conn, beat time.Duration) (*pgconn.Notification, error) {
	waitCtx, cancel := context.WithTimeout(ctx, beat)
	defer cancel()
	n, err := conn.WaitForNotification(waitCtx)
	if err != nil && ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
		return nil, errQuiet
	}
	return n, err
}

// ping checks conn answers within beat.
func ping(ctx context.Context, conn *pgx.Conn, beat time.Duration) error {
	pingCtx, cancel := context.WithTimeout(ctx, beat)
	defer cancel()
	return conn.Ping(pingCtx)
}

// openSignals opens a signal on each cache that takes one, at its index in
// Caches; nil for every other cache.
func (w RevocationWatcher) openSignals() []*limes.RevocationSignal {
	signals := make([]*limes.RevocationSignal, len(w.Caches))
	for i, c := range w.Caches {
		if t, ok := c.(limes.RevocationSignalTarget); ok {
			signals[i] = t.NewSignal()
		}
	}
	return signals
}

// closeSignals closes the signals openSignals opened.
func closeSignals(signals []*limes.RevocationSignal) {
	for _, s := range signals {
		if s != nil {
			s.Close()
		}
	}
}

// listening tells each signalled cache the watcher listens, which clears
// it, and clears every other cache.
func (w RevocationWatcher) listening(signals []*limes.RevocationSignal) {
	for i, c := range w.Caches {
		if s := signals[i]; s != nil {
			s.Listening()
			continue
		}
		c.Clear()
	}
}

// notListening tells each signalled cache the watcher stopped listening.
func notListening(signals []*limes.RevocationSignal) {
	for _, s := range signals {
		if s != nil {
			s.NotListening()
		}
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

// signal passes a notification of ids on: to a signalled cache as it is,
// and from the others drops what a revocation of ids outdates in each cache
// that can tell, clearing the rest, and every cache when ids is nil.
func (w RevocationWatcher) signal(signals []*limes.RevocationSignal, ids []uuid.UUID) {
	for i, c := range w.Caches {
		if s := signals[i]; s != nil {
			if ids == nil {
				s.RevokedAll()
			} else {
				s.Revoked(ids...)
			}
			continue
		}
		if r, ok := c.(RevokedClearer); ok && ids != nil {
			r.ClearRevoked(ids...)
			continue
		}
		c.Clear()
	}
}
