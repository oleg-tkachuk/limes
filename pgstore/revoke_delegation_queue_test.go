package pgstore

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
)

// The waits of the delegation-queue check: how long the revoke is given to
// start waiting for the insert in flight, how long a later insert is given
// to pass it, and how long the revoke may take once the first insert ends.
const (
	revokeQueueSettle = 200 * time.Millisecond
	laterInsertWait   = 300 * time.Millisecond
	revokeQueueBound  = 2 * time.Second
)

// A revoke waits for an insert of a child already holding its capability,
// and an insert of a child that comes after waits behind the revoke rather
// than passing it: inserts overlapping one another could otherwise hold the
// capability's row for as long as they kept coming, and its revoke would
// never be written.
func TestALaterDelegationWaitsBehindAWaitingRevoke(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	c := f.parent
	c.ID, c.Subject.Subject = uuid.New(), "agent:delegating"
	if err := f.records.Insert(ctx, limes.InsertRequest{Capability: c}); err != nil {
		t.Fatal(err)
	}
	// An insert of a child in flight, as Insert holds it before its row.
	inFlight, err := owner.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = inFlight.Rollback(context.WithoutCancel(t.Context())) }()
	if _, err := inFlight.Exec(t.Context(), sqlShareTenantLock, revokeLockClass, f.mine); err != nil {
		t.Fatal(err)
	}
	if _, err := inFlight.Exec(t.Context(), sqlLockParent, c.ID); err != nil {
		t.Fatal(err)
	}
	revokeCtx, cancel := context.WithTimeout(ctx, revokeQueueBound+revokeQueueSettle+laterInsertWait)
	defer cancel()
	revoked := make(chan error, 1)
	go func() {
		_, err := f.records.Revoke(revokeCtx, limes.RevokeRequest{CapabilityID: c.ID})
		revoked <- err
	}()
	time.Sleep(revokeQueueSettle)

	child := c
	child.ID, child.ParentID, child.Subject.Subject = uuid.New(), c.ID, "agent:later"
	insertCtx, cancelInsert := context.WithTimeout(ctx, laterInsertWait)
	defer cancelInsert()
	if err := f.records.Insert(insertCtx, limes.InsertRequest{Capability: child}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("an insert of a child behind a waiting revoke: err = %v, want it to wait past %s", err, laterInsertWait)
	}
	if err := inFlight.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-revoked:
		if err != nil {
			t.Errorf("the revoke once the insert in flight ended: %v", err)
		}
	case <-time.After(revokeQueueBound):
		t.Errorf("the revoke not done within %s of the insert in flight ending", revokeQueueBound)
	}
}
