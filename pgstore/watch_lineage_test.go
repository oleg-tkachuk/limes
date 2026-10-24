package pgstore

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
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
	a, err := f.records.RevocationLineage(t.Context(), ids[2])
	if err != nil || a.Revoked || !slices.Equal(a.Ancestors, []uuid.UUID{ids[1], ids[0]}) {
		t.Errorf("RevocationLineage = %+v, %v; want live under %v", a, err, ids[:2])
	}
	if a, err := f.records.RevocationLineage(t.Context(), ids[0]); err != nil || a.Revoked || len(a.Ancestors) != 0 {
		t.Errorf("RevocationLineage of a root = %+v, %v; want live and none", a, err)
	}
	if _, err := f.records.Revoke(WithTenant(t.Context(), f.mine), limes.RevokeRequest{CapabilityID: ids[0]}); err != nil {
		t.Fatal(err)
	}
	if a, err := f.records.RevocationLineage(t.Context(), ids[2]); err != nil || !a.Revoked {
		t.Errorf("RevocationLineage under a revoked root = %+v, %v; want revoked", a, err)
	}
	if a, err := f.records.RevocationLineage(t.Context(), uuid.New()); !errors.Is(err, limes.ErrNotFound) || a.Revoked || a.Ancestors != nil {
		t.Errorf("RevocationLineage of an id not on record = %+v, %v; want ErrNotFound", a, err)
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

// unsyncedCacheTTL is the TTL of the disconnected-watcher check: short, so
// an answer outlives it quickly once the watcher stops vouching.
const unsyncedCacheTTL = watchQuiet

// While the watcher is down a revocation sends a notification nobody hears,
// so a cache it kept in step serves its short TTL, not its SyncedTTL: the
// revoked capability stops verifying though the cache had answered live
// under an hour's bound. Once the watcher listens again, a revocation
// reaches the cache by notification.
func TestRevocationWatcherDownServesTheShortTTL(t *testing.T) {
	f := newTenantFixture(t)
	cache, err := limes.NewRevocationCache(limes.RevocationCacheConfig{Capabilities: f.records, TTL: unsyncedCacheTTL, SyncedTTL: cacheTTL})
	if err != nil {
		t.Fatal(err)
	}
	var down atomic.Bool
	errDown := errors.New("the database is unreachable")
	connect := func(ctx context.Context) (*pgx.Conn, error) {
		if down.Load() {
			return nil, errDown
		}
		return connectApp(ctx)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = RevocationWatcher{Connect: connect, Caches: []Clearer{cache}}.Run(ctx) }()
	await := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(watchWait)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("%s: not within %s", what, watchWait)
			}
			time.Sleep(watchQuiet / 10)
		}
	}
	revoked := func(id uuid.UUID) func() bool {
		return func() bool {
			r, err := cache.IsRevoked(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			return r
		}
	}
	await("listening", func() bool { return cache.Stats().Synced })
	ids := f.tree(t, root, root)
	if revoked(ids[0])() {
		t.Fatal("revoked before any revocation")
	}

	down.Store(true)
	if _, err := owner.Exec(t.Context(), sqlEndWatcher, watcherName); err != nil {
		t.Fatal(err)
	}
	await("the watcher stops vouching", func() bool { return !cache.Stats().Synced })
	if st := cache.Stats(); st.SyncsLost == 0 || st.LastSyncLoss.IsZero() {
		t.Errorf("Stats after the loss = %+v; want it counted and timed", st)
	}
	mine := WithTenant(t.Context(), f.mine)
	if _, err := f.records.Revoke(mine, limes.RevokeRequest{CapabilityID: ids[0]}); err != nil {
		t.Fatal(err)
	}
	await("the revocation seen while the watcher is down", revoked(ids[0]))

	down.Store(false)
	await("listening again", func() bool { st := cache.Stats(); return st.Synced && st.Syncs == 2 })
	if revoked(ids[1])() {
		t.Fatal("revoked before its revocation")
	}
	if _, err := f.records.Revoke(mine, limes.RevokeRequest{CapabilityID: ids[1]}); err != nil {
		t.Fatal(err)
	}
	await("the revocation signalled once listening again", func() bool { return cache.Stats().Signals >= 1 })
	if !revoked(ids[1])() {
		t.Error("a signalled revocation not seen")
	}
}

// sqlIsRevokedSource is limes.is_revoked's body.
const sqlIsRevokedSource = `SELECT prosrc FROM pg_proc WHERE oid = 'limes.is_revoked(uuid, integer)'::regprocedure`

// IsRevoked and RevocationLineage answer alike for every place in a chain —
// a live leaf, a revoked root's descendants, one revoked itself — as
// is_revoked reads revocation_lineage's walk; an id not on record is live to
// IsRevoked and not found to RevocationLineage.
func TestIsRevokedAnswersAsRevocationLineage(t *testing.T) {
	f := newTenantFixture(t)
	var src string
	if err := owner.QueryRow(t.Context(), sqlIsRevokedSource).Scan(&src); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(src, "limes.revocation_lineage(") {
		t.Errorf("limes.is_revoked walks the chain on its own:\n%s", src)
	}
	ids := f.tree(t, root, 0, 1, root)
	if _, err := f.records.Revoke(WithTenant(t.Context(), f.mine), limes.RevokeRequest{CapabilityID: ids[1]}); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		revoked, err := f.records.IsRevoked(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		a, err := f.records.RevocationLineage(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if revoked != a.Revoked {
			t.Errorf("%s: IsRevoked %v, RevocationLineage %v", id, revoked, a.Revoked)
		}
	}
	if revoked, err := f.records.IsRevoked(t.Context(), uuid.New()); err != nil || revoked {
		t.Errorf("IsRevoked of an id not on record = %v, %v; want live", revoked, err)
	}
}

// Over the store, a RevocationCache answers an id not on record live, as
// IsRevoked does, and keeps no answer for it: RevocationLineage's not found
// is no refusal. This also holds pgstore's release to a limes that reads it
// so.
func TestRevocationCacheKeepsNoAnswerForAnUnknownID(t *testing.T) {
	f := newTenantFixture(t)
	cache, err := limes.NewRevocationCache(limes.RevocationCacheConfig{Capabilities: f.records, TTL: cacheTTL})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if revoked, err := cache.IsRevoked(t.Context(), uuid.New()); err != nil || revoked {
			t.Fatalf("IsRevoked of an id not on record = %v, %v; want live", revoked, err)
		}
	}
	if st := cache.Stats().Capabilities; st.Entries != 0 || st.Hits != 0 {
		t.Errorf("Stats = %+v; want nothing kept for an id not on record", st)
	}
}
