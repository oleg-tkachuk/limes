package pgstore

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes"
)

// notifications listens on RevocationChannel on a connection of its own and
// returns the payloads that arrive, each in turn.
func notifications(t *testing.T) func() (string, bool) {
	t.Helper()
	conn, err := pgx.ConnectConfig(t.Context(), owner.Config().ConnConfig.Copy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	if _, err := conn.Exec(t.Context(), "LISTEN "+pgx.Identifier{RevocationChannel}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	return func() (string, bool) {
		ctx, cancel := context.WithTimeout(t.Context(), watchQuiet)
		defer cancel()
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return "", false
		}
		return n.Payload, true
	}
}

// tree inserts under tenant f.mine a capability for each parent given, in
// turn, a zero parent a root, and returns their ids.
func (f tenantFixture) tree(t *testing.T, parents ...int) []uuid.UUID {
	t.Helper()
	ctx := WithTenant(t.Context(), f.mine)
	ids := make([]uuid.UUID, len(parents))
	for i, p := range parents {
		c := f.capability(f.mine)
		if p >= 0 {
			c.ParentID = ids[p]
		}
		if err := f.records.Insert(ctx, limes.InsertRequest{Capability: c, IssuedBy: c.Subject}); err != nil {
			t.Fatal(err)
		}
		ids[i] = c.ID
	}
	return ids
}

// root is tree's parent of a capability with none.
const root = -1

// A revocation notifies with the ids a cache must drop answers for: the
// capability revoked, the top of a cascade alone, nothing for a capability
// whose parent was revoked before, and no id — clear whole — for a Biscuit
// copy's revocation or past MaxNotifiedRevocations ids.
func TestRevocationNotificationNamesWhatItOutdates(t *testing.T) {
	f := newTenantFixture(t)
	next := notifications(t)
	ctx := WithTenant(t.Context(), f.mine)
	ids := f.tree(t, root, 0, 1, root)
	expect := func(what, want string) {
		t.Helper()
		got, ok := next()
		if !ok || got != want {
			t.Errorf("%s: notified %q (%v), want %q", what, got, ok, want)
		}
		if extra, ok := next(); ok {
			t.Errorf("%s: then notified %q too", what, extra)
		}
	}
	if _, err := f.records.Revoke(ctx, limes.RevokeRequest{CapabilityID: ids[0], CascadeChildren: true}); err != nil {
		t.Fatal(err)
	}
	expect("a cascade", ids[0].String())
	if _, err := f.records.Revoke(ctx, limes.RevokeRequest{CapabilityID: ids[3]}); err != nil {
		t.Fatal(err)
	}
	expect("a root", ids[3].String())
	under := f.tree(t, root, 0)
	if _, err := f.records.Revoke(ctx, limes.RevokeRequest{CapabilityID: under[0]}); err != nil {
		t.Fatal(err)
	}
	expect("a parent", under[0].String())
	if _, err := f.records.Revoke(ctx, limes.RevokeRequest{CapabilityID: under[1]}); err != nil {
		t.Fatal(err)
	}
	if got, ok := next(); ok {
		t.Errorf("a child of a revoked parent: notified %q", got)
	}
	if err := f.records.RevokeBiscuit(ctx, limes.RevokeBiscuitRequest{Copy: biscuitCopy(t, under[1], []byte("notified-copy"))}); err != nil {
		t.Fatal(err)
	}
	expect("a Biscuit copy", "")
	for _, n := range []int{MaxNotifiedRevocations, MaxNotifiedRevocations + 1} {
		roots := f.tree(t, slices.Repeat([]int{root}, n)...)
		if _, err := owner.Exec(t.Context(), `
INSERT INTO limes.revocations (capability_id, tenant_id, revoked_at, note, actor, cascaded)
SELECT id, $2, now(), 'many', 'test', false FROM unnest($1::uuid[]) id`, roots, f.mine); err != nil {
			t.Fatal(err)
		}
		got, ok := next()
		named := strings.Fields(got)
		switch {
		case !ok:
			t.Errorf("%d revoked at once: no notification", n)
		case n <= MaxNotifiedRevocations && len(named) != n:
			t.Errorf("%d revoked at once: %d ids named", n, len(named))
		case n > MaxNotifiedRevocations && got != "":
			t.Errorf("%d revoked at once: %d ids named, want none", n, len(named))
		}
	}
}

// RevocationLineage answers as IsRevoked does, and names the ancestors on
// record, nearest first.
func TestRevocationLineageNamesTheAncestors(t *testing.T) {
	f := newTenantFixture(t)
	ids := f.tree(t, root, 0, 1)
	revoked, ancestors, err := f.records.RevocationLineage(t.Context(), ids[2])
	if err != nil || revoked || !slices.Equal(ancestors, []uuid.UUID{ids[1], ids[0]}) {
		t.Errorf("RevocationLineage = %v, %v, %v; want live under %v", revoked, ancestors, err, ids[:2])
	}
	if revoked, ancestors, err := f.records.RevocationLineage(t.Context(), ids[0]); err != nil || revoked || len(ancestors) != 0 {
		t.Errorf("RevocationLineage of a root = %v, %v, %v; want live and none", revoked, ancestors, err)
	}
	if _, err := f.records.Revoke(WithTenant(t.Context(), f.mine), limes.RevokeRequest{CapabilityID: ids[0]}); err != nil {
		t.Fatal(err)
	}
	if revoked, _, err := f.records.RevocationLineage(t.Context(), ids[2]); err != nil || !revoked {
		t.Errorf("RevocationLineage under a revoked root = %v, %v; want revoked", revoked, err)
	}
	if revoked, _, err := f.records.RevocationLineage(t.Context(), uuid.New()); err != nil || revoked {
		t.Errorf("RevocationLineage of an id not on record = %v, %v; want live", revoked, err)
	}
}

// cacheTTL outlives each check, so only the watcher ends an answer.
const cacheTTL = time.Hour

// Over a RevocationCache, the watcher drops what a revocation outdates and
// no more: a check of an unrelated capability is still answered from the
// cache, and a descendant of the revoked one is asked again and revoked.
func TestRevocationWatcherKeepsUnrelatedAnswers(t *testing.T) {
	f := newTenantFixture(t)
	cache, err := limes.NewRevocationCache(limes.RevocationCacheConfig{Capabilities: f.records, TTL: cacheTTL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = RevocationWatcher{Connect: connectApp, Caches: []Clearer{cache}}.Run(ctx) }()
	await := func(cond func(limes.RevocationCacheStats) bool, what string) {
		t.Helper()
		deadline := time.Now().Add(watchWait)
		for !cond(cache.Stats()) {
			if time.Now().After(deadline) {
				t.Fatalf("%s: not within %s", what, watchWait)
			}
			time.Sleep(watchQuiet / 10)
		}
	}
	await(func(st limes.RevocationCacheStats) bool { return st.Clears == 1 }, "the clear once listening")
	ids := f.tree(t, root, 0, root)
	for _, id := range ids {
		if revoked, err := cache.IsRevoked(t.Context(), id); err != nil || revoked {
			t.Fatalf("IsRevoked %s = %v, %v", id, revoked, err)
		}
	}
	if _, err := f.records.Revoke(WithTenant(t.Context(), f.mine), limes.RevokeRequest{CapabilityID: ids[0]}); err != nil {
		t.Fatal(err)
	}
	await(func(st limes.RevocationCacheStats) bool { return st.ClearsRevoked == 1 }, "ClearRevoked")
	before := cache.Stats().Capabilities
	if revoked, err := cache.IsRevoked(t.Context(), ids[2]); err != nil || revoked {
		t.Errorf("the unrelated capability = %v, %v; want live", revoked, err)
	}
	if revoked, err := cache.IsRevoked(t.Context(), ids[1]); err != nil || !revoked {
		t.Errorf("the revoked capability's child = %v, %v; want revoked", revoked, err)
	}
	after := cache.Stats().Capabilities
	if after.Hits-before.Hits != 1 || after.Lookups-before.Lookups != 1 {
		t.Errorf("after the revocation: %d hits, %d lookups; want the unrelated one a hit and the child looked up",
			after.Hits-before.Hits, after.Lookups-before.Lookups)
	}
	if st := cache.Stats(); st.Clears != 1 {
		t.Errorf("%d clears; want only the one once listening", st.Clears)
	}
}
