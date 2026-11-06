package limes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"testing"
	"time"
	"uuid"
)

// revokerCacheTTL outlasts every test: an answer the cache holds goes only
// when something clears it.
const revokerCacheTTL = time.Hour

// fakeRevocations is a store for a Revoker: each method answers with the
// result set on it, or err, and a revocation it carries out is read back by
// IsRevoked and IsBiscuitRevoked.
type fakeRevocations struct {
	revokeRes   RevokeResult
	matchingRes RevokeMatchingResult
	biscuitRes  RevokeBiscuitResult
	err         error
	// matched is the capability RevokeMatching revokes.
	matched  uuid.UUID
	revoked  map[uuid.UUID]bool
	biscuits map[string]bool
	calls    int
}

func newFakeRevocations() *fakeRevocations {
	return &fakeRevocations{revoked: map[uuid.UUID]bool{}, biscuits: map[string]bool{}}
}

func (f *fakeRevocations) Revoke(_ context.Context, req RevokeRequest) (RevokeResult, error) {
	f.calls++
	if f.err != nil {
		return RevokeResult{}, f.err
	}
	f.revoked[req.CapabilityID] = true
	return f.revokeRes, nil
}

func (f *fakeRevocations) RevokeMatching(context.Context, RevokeMatchingRequest) (RevokeMatchingResult, error) {
	f.calls++
	if f.err != nil {
		return RevokeMatchingResult{}, f.err
	}
	f.revoked[f.matched] = true
	return f.matchingRes, nil
}

func (f *fakeRevocations) RevokeBiscuit(_ context.Context, req RevokeBiscuitRequest) (RevokeBiscuitResult, error) {
	f.calls++
	if f.err != nil {
		return RevokeBiscuitResult{}, f.err
	}
	f.biscuits[string(req.Copy.revocationID)] = true
	return f.biscuitRes, nil
}

func (f *fakeRevocations) IsRevoked(_ context.Context, id uuid.UUID) (bool, error) {
	return f.revoked[id], nil
}

func (f *fakeRevocations) IsBiscuitRevoked(_ context.Context, ids [][]byte) (bool, error) {
	return slices.ContainsFunc(ids, func(id []byte) bool { return f.biscuits[string(id)] }), nil
}

func (f *fakeRevocations) GetBiscuitRevocation(context.Context, BiscuitCopy) (BiscuitRevocation, error) {
	return BiscuitRevocation{}, ErrNotFound
}

// revokerEvents records every event a Revoker makes.
type revokerEvents struct {
	revoked         []RevokedEvent
	refused, failed []RevokerErrorEvent
}

func (e *revokerEvents) hooks() RevokerEvents {
	return RevokerEvents{
		OnRevoked: func(_ context.Context, ev RevokedEvent) { e.revoked = append(e.revoked, ev) },
		OnRefused: func(_ context.Context, ev RevokerErrorEvent) { e.refused = append(e.refused, ev) },
		OnFailed:  func(_ context.Context, ev RevokerErrorEvent) { e.failed = append(e.failed, ev) },
	}
}

func newTestRevoker(t *testing.T, cfg RevokerConfig) *Revoker {
	t.Helper()
	r, err := NewRevoker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// testBiscuitCopy is a copy of capability id, as Verifier.BiscuitCopy reads one.
func testBiscuitCopy(id uuid.UUID) BiscuitCopy {
	return BiscuitCopy{capabilityID: id, revocationID: []byte("block-signature")}
}

// NewRevoker refuses no store, and a store, bulk revoker or Biscuit store
// holding a nil value, which would panic at the first revocation.
func TestNewRevokerRefusesMiswiring(t *testing.T) {
	var nilStore *fakeRevocations
	for _, tc := range []struct {
		name string
		cfg  RevokerConfig
	}{
		{"no store", RevokerConfig{}},
		{"a nil store", RevokerConfig{Store: nilStore}},
		{"a nil bulk revoker", RevokerConfig{Store: newFakeRevocations(), Bulk: nilStore}},
		{"a nil Biscuit store", RevokerConfig{Store: newFakeRevocations(), Biscuits: nilStore}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewRevoker(tc.cfg); !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("NewRevoker = %v, want ErrInvalidConfig", err)
			}
		})
	}
	if _, err := NewRevoker(RevokerConfig{Store: newFakeRevocations()}); err != nil {
		t.Errorf("NewRevoker over a store alone = %v", err)
	}
}

// revokerCall runs one of a Revoker's methods and returns its result, for
// comparison with what the store answered.
type revokerCall struct {
	op  RevokerOperation
	run func(context.Context, *Revoker) (any, error)
	// want is the store's answer the call must return.
	want func(*fakeRevocations) any
}

// revokerCalls are the three methods, over capability id in tenant.
func revokerCalls(id, tenant uuid.UUID) []revokerCall {
	return []revokerCall{
		{RevokerOperationRevoke, func(ctx context.Context, r *Revoker) (any, error) {
			return r.Revoke(ctx, RevokeRequest{CapabilityID: id, Actor: "op", Note: "leak"})
		}, func(f *fakeRevocations) any { return f.revokeRes }},
		{RevokerOperationRevokeMatching, func(ctx context.Context, r *Revoker) (any, error) {
			return r.RevokeMatching(ctx, RevokeMatchingRequest{TenantID: tenant, Subject: "user", Actor: "op", Note: "left"})
		}, func(f *fakeRevocations) any { return f.matchingRes }},
		{RevokerOperationRevokeBiscuit, func(ctx context.Context, r *Revoker) (any, error) {
			return r.RevokeBiscuit(ctx, RevokeBiscuitRequest{Copy: testBiscuitCopy(id), Actor: "op", Note: "leak"})
		}, func(f *fakeRevocations) any { return f.biscuitRes }},
	}
}

// Each method returns what the store answered, unchanged, and logs and
// reports it: carried out at INFO and OnRevoked, refused at WARN and
// OnRefused, failed at ERROR and OnFailed.
func TestRevokerDelegatesLogsAndReports(t *testing.T) {
	ctx := context.Background()
	id, tenant := uuid.New(), uuid.New()
	storeFailure := errors.New("connection reset")
	for _, outcome := range []struct {
		name  string
		err   error
		level slog.Level
		msg   string
		code  any
	}{
		{"carried out", nil, slog.LevelInfo, msgRevoked, nil},
		{"refused", notOnRecord(id), slog.LevelWarn, msgRevokerRefused, string(CodeNotFound)},
		{"failed", storeFailure, slog.LevelError, msgRevokerFailed, nil},
	} {
		for _, call := range revokerCalls(id, tenant) {
			t.Run(outcome.name+"/"+string(call.op), func(t *testing.T) {
				store := newFakeRevocations()
				store.err = outcome.err
				store.revokeRes = RevokeResult{AlreadyRevoked: true, PriorDisposition: RecordDispositionRevoked}
				store.matchingRes = RevokeMatchingResult{Revoked: 3, Tenants: []uuid.UUID{tenant}, More: true}
				store.biscuitRes = RevokeBiscuitResult{AlreadyRevoked: true}
				var logs logCapture
				var events revokerEvents
				r := newTestRevoker(t, RevokerConfig{Store: store, Bulk: store, Biscuits: store, Logger: logs.logger(), Events: events.hooks()})

				got, err := call.run(ctx, r)
				if !errors.Is(err, outcome.err) || (outcome.err == nil) != (err == nil) {
					t.Fatalf("err = %v, want the store's %v", err, outcome.err)
				}
				if store.calls != 1 {
					t.Errorf("the store was called %d times, want 1", store.calls)
				}
				if outcome.err == nil {
					if want := call.want(store); !resultsEqual(got, want) {
						t.Errorf("result = %+v, want the store's %+v", got, want)
					}
				}
				wantAttrs(t, string(call.op), logs.only(t), map[string]any{
					slog.LevelKey: outcome.level.String(), slog.MessageKey: outcome.msg,
					logKeyOperation: string(call.op), logKeyActor: "op", logKeyCode: outcome.code,
				})
				wantEvents := map[string]int{"revoked": len(events.revoked), "refused": len(events.refused), "failed": len(events.failed)}
				fired := map[slog.Level]string{slog.LevelInfo: "revoked", slog.LevelWarn: "refused", slog.LevelError: "failed"}[outcome.level]
				for name, n := range wantEvents {
					if want := map[bool]int{true: 1}[name == fired]; n != want {
						t.Errorf("%s events: %d, want %d", name, n, want)
					}
				}
				for _, ev := range slices.Concat(events.refused, events.failed) {
					if ev.Operation != call.op || !errors.Is(ev.Err, err) {
						t.Errorf("error event %+v, want op %s and the call's error", ev, call.op)
					}
				}
			})
		}
	}
}

// notOnRecord is the store's refusal of an id not on record.
func notOnRecord(id uuid.UUID) error { return fmt.Errorf("revoke %s: %w", id, ErrNotFound) }

// resultsEqual compares two of the store's results, whichever type.
func resultsEqual(got, want any) bool {
	switch w := want.(type) {
	case RevokeResult:
		g, ok := got.(RevokeResult)
		return ok && g.AlreadyRevoked == w.AlreadyRevoked && g.PriorDisposition == w.PriorDisposition
	case RevokeMatchingResult:
		g, ok := got.(RevokeMatchingResult)
		return ok && g.Revoked == w.Revoked && slices.Equal(g.Tenants, w.Tenants) && g.More == w.More
	case RevokeBiscuitResult:
		g, ok := got.(RevokeBiscuitResult)
		return ok && g.AlreadyRevoked == w.AlreadyRevoked
	}
	return false
}

// A revocation's line names what was revoked, by whom and why, and what the
// store found and wrote; its event carries the same.
func TestRevokerLogsWhatItRevoked(t *testing.T) {
	ctx := context.Background()
	id, tenant, run := uuid.New(), uuid.New(), uuid.New()
	store := newFakeRevocations()
	store.revokeRes = RevokeResult{PriorDisposition: RecordDispositionActive}
	store.matchingRes = RevokeMatchingResult{Revoked: 2, Tenants: []uuid.UUID{tenant}, More: true}
	var logs logCapture
	var events revokerEvents
	r := newTestRevoker(t, RevokerConfig{Store: store, Bulk: store, Biscuits: store, Logger: logs.logger(), Events: events.hooks()})

	if _, err := r.Revoke(ctx, RevokeRequest{CapabilityID: id, Actor: "alice", Note: "compromise", CascadeChildren: true}); err != nil {
		t.Fatal(err)
	}
	wantAttrs(t, "revoke", logs.only(t), map[string]any{
		logKeyCapability: id.String(), logKeyActor: "alice", logKeyNote: "compromise", logKeyCascade: true,
		logKeyAlreadyRevoked: false, logKeyPrior: string(RecordDispositionActive),
	})

	logs.reset()
	if _, err := r.RevokeMatching(ctx, RevokeMatchingRequest{TenantID: tenant, Subject: "bob", RunID: run, Actor: "alice", Note: "left"}); err != nil {
		t.Fatal(err)
	}
	rec := logs.only(t)
	wantAttrs(t, "revoke matching", rec, map[string]any{
		logKeyTenant: tenant.String(), logKeySubject: "bob", logKeyRun: run.String(), logKeyClient: nil, logKeySession: nil,
		logKeyAllTenants: nil, logKeyRevoked: float64(2), logKeyMore: true, logKeyNote: "left",
	})
	if tenants, _ := rec[logKeyTenants].([]any); len(tenants) != 1 || tenants[0] != tenant.String() {
		t.Errorf("tenants = %v, want [%s]", rec[logKeyTenants], tenant)
	}

	logs.reset()
	if _, err := r.RevokeMatching(ctx, RevokeMatchingRequest{AllTenants: true, ClientID: "c", Actor: "alice"}); err != nil {
		t.Fatal(err)
	}
	wantAttrs(t, "revoke matching across tenants", logs.only(t), map[string]any{
		logKeyAllTenants: true, logKeyTenant: nil, logKeyClient: "c",
	})

	logs.reset()
	if _, err := r.RevokeBiscuit(ctx, RevokeBiscuitRequest{Copy: testBiscuitCopy(id), Actor: "alice"}); err != nil {
		t.Fatal(err)
	}
	wantAttrs(t, "revoke biscuit", logs.only(t), map[string]any{
		logKeyOperation: string(RevokerOperationRevokeBiscuit), logKeyCapability: id.String(), logKeyAlreadyRevoked: false,
	})

	if len(events.revoked) != 4 {
		t.Fatalf("OnRevoked called %d times, want 4", len(events.revoked))
	}
	one, bulk, across, sealed := events.revoked[0], events.revoked[1], events.revoked[2], events.revoked[3]
	if one.Operation != RevokerOperationRevoke || one.CapabilityID != id || one.Actor != "alice" || one.Note != "compromise" ||
		one.PriorDisposition != RecordDispositionActive || one.AlreadyRevoked {
		t.Errorf("revoke event %+v", one)
	}
	if bulk.Operation != RevokerOperationRevokeMatching || bulk.TenantID != tenant || bulk.Revoked != 2 || !bulk.More ||
		!slices.Equal(bulk.Tenants, []uuid.UUID{tenant}) || bulk.CapabilityID != uuid.Nil() {
		t.Errorf("revoke matching event %+v", bulk)
	}
	if across.TenantID != uuid.Nil() {
		t.Errorf("revoke matching across tenants names tenant %s", across.TenantID)
	}
	if sealed.Operation != RevokerOperationRevokeBiscuit || sealed.CapabilityID != id {
		t.Errorf("revoke biscuit event %+v", sealed)
	}
	// The event's tenants are its own: the store's slice stays as it was.
	bulk.Tenants[0] = uuid.Nil()
	if store.matchingRes.Tenants[0] != tenant {
		t.Error("the event shares the result's tenants")
	}
}

// A method whose store the revoker was not given fails as ErrInvalidConfig,
// logged at ERROR and reported to OnFailed, without calling any store.
func TestRevokerWithoutAFeature(t *testing.T) {
	ctx := context.Background()
	id := uuid.New()
	store := newFakeRevocations()
	var logs logCapture
	var events revokerEvents
	r := newTestRevoker(t, RevokerConfig{Store: store, Logger: logs.logger(), Events: events.hooks()})
	for _, call := range revokerCalls(id, uuid.New())[1:] {
		logs.reset()
		if _, err := call.run(ctx, r); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%s = %v, want ErrInvalidConfig", call.op, err)
		}
		wantAttrs(t, string(call.op), logs.only(t), map[string]any{
			slog.LevelKey: slog.LevelError.String(), slog.MessageKey: msgRevokerFailed,
			logKeyOperation: string(call.op), logKeyCode: string(CodeInvalidConfig),
		})
	}
	if store.calls != 0 {
		t.Errorf("a store was called %d times", store.calls)
	}
	if len(events.failed) != 2 || events.failed[0].Code != CodeInvalidConfig || events.failed[1].Code != CodeInvalidConfig {
		t.Errorf("OnFailed got %+v", events.failed)
	}
}

// A nil Logger logs to slog.Default(), resolved at each call.
func TestRevokerLogsToTheDefault(t *testing.T) {
	var logs logCapture
	previous := slog.Default()
	slog.SetDefault(logs.logger())
	t.Cleanup(func() { slog.SetDefault(previous) })
	r := newTestRevoker(t, RevokerConfig{Store: newFakeRevocations()})
	if _, err := r.Revoke(context.Background(), RevokeRequest{CapabilityID: uuid.New()}); err != nil {
		t.Fatal(err)
	}
	wantAttrs(t, "revoke", logs.only(t), map[string]any{slog.MessageKey: msgRevoked})
}

// A cached "not revoked" answer becomes "revoked" as soon as the revoker
// returns, through each method: without the cache the revoker was given, it
// would stand for the TTL.
func TestRevokerClearsTheCache(t *testing.T) {
	ctx := context.Background()
	discard := slog.New(slog.DiscardHandler)
	for _, given := range []bool{true, false} {
		id := uuid.New()
		for _, call := range revokerCalls(id, uuid.New()) {
			store := newFakeRevocations()
			store.matched = id
			cache, err := NewRevocationCache(RevocationCacheConfig{Capabilities: store, Biscuits: store, TTL: revokerCacheTTL, Logger: discard})
			if err != nil {
				t.Fatal(err)
			}
			cfg := RevokerConfig{Store: store, Bulk: store, Biscuits: store, Logger: discard}
			if given {
				cfg.Cache = cache
			}
			revoked := func() bool {
				t.Helper()
				got, err := cache.IsRevoked(ctx, id)
				if call.op == RevokerOperationRevokeBiscuit {
					got, err = cache.IsBiscuitRevoked(ctx, [][]byte{testBiscuitCopy(id).revocationID})
				}
				if err != nil {
					t.Fatal(err)
				}
				return got
			}
			if revoked() {
				t.Fatalf("%s: revoked before the revocation", call.op)
			}
			if _, err := call.run(ctx, newTestRevoker(t, cfg)); err != nil {
				t.Fatal(err)
			}
			if got := revoked(); got != given {
				t.Errorf("%s with the cache given %v: revoked = %v right after, want %v", call.op, given, got, given)
			}
		}
	}
}

// A failed revocation clears nothing: the answers the cache holds stand.
func TestRevokerClearsNothingOnFailure(t *testing.T) {
	ctx := context.Background()
	store := newFakeRevocations()
	cache, err := NewRevocationCache(RevocationCacheConfig{Capabilities: store, TTL: revokerCacheTTL})
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	if _, err := cache.IsRevoked(ctx, id); err != nil {
		t.Fatal(err)
	}
	store.err = errors.New("connection reset")
	r := newTestRevoker(t, RevokerConfig{Store: store, Bulk: store, Biscuits: store, Cache: cache, Logger: slog.New(slog.DiscardHandler)})
	for _, call := range revokerCalls(id, uuid.New()) {
		if _, err := call.run(ctx, r); err == nil {
			t.Fatalf("%s succeeded over a failing store", call.op)
		}
	}
	if stats := cache.Stats(); stats.Clears != 0 {
		t.Errorf("the cache was cleared %d times after failures", stats.Clears)
	}
}

// slowRevoke revokes as its fakeRevocations does, after slowStoreDelay.
type slowRevoke struct{ *fakeRevocations }

func (s slowRevoke) Revoke(ctx context.Context, req RevokeRequest) (RevokeResult, error) {
	time.Sleep(slowStoreDelay)
	return s.fakeRevocations.Revoke(ctx, req)
}

// A revocation's event carries how long the call took, carried out or not.
func TestRevokerReportsTheCallsDuration(t *testing.T) {
	for name, err := range map[string]error{"revoked": nil, "refused": notOnRecord(uuid.New()), "failed": errors.New("store down")} {
		t.Run(name, func(t *testing.T) {
			store := newFakeRevocations()
			store.err = err
			var ev revokerEvents
			r := newTestRevoker(t, RevokerConfig{Store: slowRevoke{store}, Logger: slog.New(slog.DiscardHandler), Events: ev.hooks()})
			_, _ = r.Revoke(context.Background(), RevokeRequest{CapabilityID: uuid.New(), Actor: "ops"})
			var got []time.Duration
			for _, e := range ev.revoked {
				got = append(got, e.Duration)
			}
			for _, e := range append(ev.refused, ev.failed...) {
				got = append(got, e.Duration)
			}
			if len(got) != 1 || got[0] < slowStoreDelay {
				t.Fatalf("durations = %v, want one of at least %s", got, slowStoreDelay)
			}
		})
	}
}
