package pgstore

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// watchWait bounds how long a check waits for the watcher to clear.
const watchWait = 10 * time.Second

// watcherName marks the watcher's connection, so a check can end it.
const watcherName = "limes-revocation-watcher-test"

// clearCounter is a cache that reports each Clear on a channel.
type clearCounter chan struct{}

func (c clearCounter) Clear() { c <- struct{}{} }

// awaitClear waits for the next Clear.
func awaitClear(t *testing.T, c clearCounter, after string) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(watchWait):
		t.Fatalf("no Clear %s within %s", after, watchWait)
	}
}

// connectApp opens a connection as the runtime role, named watcherName.
func connectApp(ctx context.Context) (*pgx.Conn, error) {
	cfg := owner.Config().ConnConfig.Copy()
	cfg.User, cfg.Password = appUser, appPassword
	cfg.RuntimeParams["application_name"] = watcherName
	return pgx.ConnectConfig(ctx, cfg)
}

// sqlEndWatcher ends the watcher's connection, as a failover would.
const sqlEndWatcher = `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name = $1`

// The watcher clears once it listens, on each revocation of a capability
// or of a copy, and again after it connects anew; it stops with its context.
func TestRevocationWatcherClearsOnEveryRevocation(t *testing.T) {
	f := newTenantFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cleared := make(clearCounter)
	var mu sync.Mutex
	var failures []error
	done := make(chan error, 1)
	go func() {
		done <- RevocationWatcher{
			Connect: connectApp,
			Caches:  []Clearer{cleared},
			OnError: func(err error) { mu.Lock(); failures = append(failures, err); mu.Unlock() },
		}.Run(ctx)
	}()
	awaitClear(t, cleared, "once listening")
	mine := WithTenant(t.Context(), f.mine)
	c := f.capability(f.mine)
	if err := f.records.Insert(mine, limes.InsertRequest{Capability: c, IssuedBy: c.Subject}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.records.Revoke(mine, limes.RevokeRequest{CapabilityID: c.ID}); err != nil {
		t.Fatal(err)
	}
	awaitClear(t, cleared, "after Revoke")
	if _, err := f.records.RevokeBiscuit(mine, limes.RevokeBiscuitRequest{Copy: biscuitCopy(t, f.child.ID, []byte("watched-copy"))}); err != nil {
		t.Fatal(err)
	}
	awaitClear(t, cleared, "after RevokeBiscuit")
	if _, err := owner.Exec(t.Context(), sqlEndWatcher, watcherName); err != nil {
		t.Fatal(err)
	}
	awaitClear(t, cleared, "after connecting again")
	mu.Lock()
	if len(failures) == 0 {
		t.Error("OnError heard nothing of the ended connection")
	}
	mu.Unlock()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Run after its context ended = %v, want context.Canceled", err)
	}
}

// Writing no revocation notifies nothing: a record, a charge or a
// revocation already on record leaves the caches alone.
func TestRevocationWatcherIgnoresOtherWrites(t *testing.T) {
	f := newTenantFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cleared := make(clearCounter, 1)
	go func() { _ = RevocationWatcher{Connect: connectApp, Caches: []Clearer{cleared}}.Run(ctx) }()
	awaitClear(t, cleared, "once listening")
	mine := WithTenant(t.Context(), f.mine)
	c := f.parent
	c.ID, c.ParentID = uuid.New(), uuid.Nil()
	if err := f.records.Insert(mine, limes.InsertRequest{Capability: c}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.usage.Charge(mine, meter.ChargeRequest{Subject: meterSubject(c.ID, f.mine), Amount: 1}, nil); err != nil {
		t.Fatal(err)
	}
	// The parent's revocation is on record already, so revoking it again
	// writes nothing.
	if _, err := f.records.Revoke(mine, limes.RevokeRequest{CapabilityID: f.parent.ID}); err != nil {
		t.Fatal(err)
	}
	// A revocation after them is the next notification the watcher hears.
	if _, err := f.records.Revoke(mine, limes.RevokeRequest{CapabilityID: c.ID}); err != nil {
		t.Fatal(err)
	}
	awaitClear(t, cleared, "after Revoke")
	select {
	case <-cleared:
		t.Error("a Clear for a write that revoked nothing")
	case <-time.After(watchQuiet):
	}
}

// watchQuiet is how long a check listens for a Clear that must not come.
const watchQuiet = 200 * time.Millisecond

// A watcher with no way to connect is refused at once.
func TestRevocationWatcherRequiresConnect(t *testing.T) {
	if err := (RevocationWatcher{}).Run(t.Context()); !errors.Is(err, errNoConnect) {
		t.Errorf("Run without Connect = %v, want errNoConnect", err)
	}
}
