package memstore

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// errBumpCallback is what onBumped returns on a FuzzLeases step whose
// callback fails: a bump that returns it must have counted nothing.
var errBumpCallback = errors.New("fuzz: onBumped failed")

// bumpRefPrefix names the ExternalRef of a keyed bump step; the step's
// amount picks which of a few refs, so a script sends refs again.
const bumpRefPrefix = "bump-"

// conflictCopyID is the revocation id of the Biscuit copy a conflict step
// sends a ref again under; the capability's bumps carry none.
var conflictCopyID = []byte("fuzz-conflict-copy")

// bumpKey is a keyed bump's pair: the capability, by its node, and the
// ref, by its number.
type bumpKey struct {
	node int
	ref  int64
}

// modelBump is what the model holds of a keyed bump the store counted: the
// count its receipt gave, when, and whether a purge of the ledger may have
// taken it since.
type modelBump struct {
	count     int64
	at        time.Time
	mayBeGone bool
}

// bumpKeys is every keyed bump the store is held to replay.
type bumpKeys map[bumpKey]*modelBump

func bumpRef(ref int64) string { return bumpRefPrefix + strconv.FormatInt(ref, 10) }

// bump sends a keyed bump of node under ref, whose onBumped fails when
// fail is set, and holds the store to the meter's contract: a ref it
// counted replays its first receipt, runs no onBumped and counts nothing;
// a ref it may have purged either replays or counts afresh; a fresh bump
// runs onBumped once, with the receipt it returns, and counts on the
// model, unless onBumped failed, which counts nothing and leaves the ref
// free. It returns a refusal for the caller to tell from a failure.
func (k bumpKeys) bump(ctx context.Context, t *testing.T, u *UsageStore, subject limes.MeterSubject, node int, ref int64, fail bool, now time.Time, m *leaseModel, step int) error {
	t.Helper()
	calls := 0
	var seen meter.Bumped
	onBumped := func(_ context.Context, b meter.Bumped) error {
		calls++
		seen = b
		if fail {
			return errBumpCallback
		}
		return nil
	}
	key := bumpKey{node: node, ref: ref}
	known := k[key]
	r, err := u.Bump(ctx, meter.BumpRequest{Subject: subject, ExternalRef: bumpRef(ref)}, onBumped)
	if err == nil && r.Replayed {
		if known == nil || r.RequestCount != known.count || calls != 0 {
			t.Fatalf("step %d: replay of %v = %+v, onBumped ran %d times; want the receipt the model holds, %+v, and no run", step, key, r, calls, known)
		}
		return nil
	}
	if known != nil && !known.mayBeGone {
		t.Fatalf("step %d: %v sent again = %+v, %v; want a replay of %+v", step, key, r, err, *known)
	}
	switch {
	case err == nil && fail:
		t.Fatalf("step %d: %v counted %+v although onBumped failed", step, key, r)
	case err == nil:
		if calls != 1 || seen.Receipt.RequestCount != r.RequestCount || seen.Receipt.Replayed {
			t.Fatalf("step %d: %v = %+v, onBumped ran %d times and saw %+v; want one run seeing the receipt", step, key, r, calls, seen.Receipt)
		}
		m.bump(node)
		k[key] = &modelBump{count: r.RequestCount, at: now}
		return nil
	case fail && errors.Is(err, errBumpCallback):
		if calls != 1 {
			t.Fatalf("step %d: %v returned the callback's error, which ran %d times", step, key, calls)
		}
		delete(k, key)
		return nil
	case isLeaseRefusal(err) && calls == 0:
		return err
	}
	t.Fatalf("step %d: %v = %+v, %v, onBumped ran %d times", step, key, r, err, calls)
	return nil
}

// conflict sends a ref the store holds again for another tenant, or under
// a Biscuit copy the first bump did not carry, and holds the store to
// ErrExternalRefConflict, with no onBumped run; the audit holds it to
// having counted nothing. A ref the model does not hold for certain is
// skipped: a fresh one would just count.
func (k bumpKeys) conflict(ctx context.Context, t *testing.T, u *UsageStore, tenant, id uuid.UUID, node int, ref int64, step int) {
	t.Helper()
	known := k[bumpKey{node: node, ref: ref}]
	if known == nil || known.mayBeGone {
		return
	}
	spec := subjectSpec{capID: id, tenant: uuid.New()}
	if ref%2 == 0 {
		spec = subjectSpec{capID: id, tenant: tenant, copies: []limes.CopyCeiling{{RevocationID: conflictCopyID}}}
	}
	ran := false
	r, err := u.Bump(ctx, meter.BumpRequest{Subject: newSubject(spec), ExternalRef: bumpRef(ref)}, func(context.Context, meter.Bumped) error {
		ran = true
		return nil
	})
	if !errors.Is(err, meter.ErrExternalRefConflict) || !errors.Is(err, limes.ErrConflict) || ran {
		t.Fatalf("step %d: ref %d of node %d sent again by another = %+v, %v, onBumped ran: %v; want ErrExternalRefConflict and no run", step, ref, node, r, err, ran)
	}
}

// deleteUsage forgets node's keyed bumps, which DeleteUsage removes with
// its counters.
func (k bumpKeys) deleteUsage(node int) {
	for key := range k {
		if key.node == node {
			delete(k, key)
		}
	}
}

// aged marks every keyed bump older than cutoff as one a purge may have
// taken, and returns how many are.
func (k bumpKeys) aged(cutoff time.Time) int64 {
	var due int64
	for _, b := range k {
		if b.at.Before(cutoff) {
			b.mayBeGone = true
			due++
		}
	}
	return due
}
