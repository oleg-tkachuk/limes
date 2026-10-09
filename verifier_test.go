package limes

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// memStore is a goroutine-safe in-memory Store stub for tests. We avoid
// pulling in the postgres package here; the SQL surface is tested
// separately and the verifier doesn't need a real DB.
type memStore struct {
	caps     map[uuid.UUID]Capability
	revoked  map[uuid.UUID]bool
	copies   map[string]bool // revoked Biscuit copies, by revocation id
	getCalls int64
	revCalls int64
}

func newMemStore() *memStore {
	return &memStore{
		caps:    map[uuid.UUID]Capability{},
		revoked: map[uuid.UUID]bool{},
		copies:  map[string]bool{},
	}
}

func (m *memStore) Insert(_ context.Context, c Capability, _ Principal) error {
	m.caps[c.ID] = c
	return nil
}
func (m *memStore) Get(_ context.Context, id uuid.UUID) (Capability, error) {
	atomic.AddInt64(&m.getCalls, 1)
	c, ok := m.caps[id]
	if !ok {
		return Capability{}, errors.New("not found")
	}
	return c, nil
}
func (m *memStore) GetRecord(ctx context.Context, id uuid.UUID) (Record, error) {
	c, err := m.Get(ctx, id)
	if err != nil {
		return Record{}, err
	}
	return Record{Capability: c}, nil
}
func (m *memStore) IsRevoked(_ context.Context, id uuid.UUID) (bool, error) {
	atomic.AddInt64(&m.revCalls, 1)
	return m.revoked[id], nil
}
func (m *memStore) Revoke(_ context.Context, args RevokeRequest) error {
	m.revoked[args.ID] = true
	return nil
}
func (m *memStore) IsBiscuitRevoked(_ context.Context, ids [][]byte) (bool, error) {
	for _, id := range ids {
		if m.copies[string(id)] {
			return true, nil
		}
	}
	return false, nil
}
func (m *memStore) RevokeBiscuit(_ context.Context, args RevokeBiscuitRequest) error {
	m.copies[string(args.RevocationID)] = true
	return nil
}
func (m *memStore) PurgeExpired(context.Context, time.Duration) (int64, error) {
	return 0, nil
}
func (m *memStore) ListByPrincipal(context.Context, ListByPrincipalRequest) ([]Capability, string, error) {
	return nil, "", nil
}

// buildIssuerVerifier wires a fresh keypair, signer, store, cache, and
// verifier — the standard test fixture.
func buildIssuerVerifier(t *testing.T) (*Issuer, *StandardVerifier, *memStore, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := NewEd25519Signer("k1", priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	store := newMemStore()
	issuer, err := NewIssuer(IssuerConfig{
		Signer:     signer,
		Store:      store,
		IssuerName: "paladin-test",
		DefaultTTL: 15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("issuer: %v", err)
	}
	cache := NewCachedRevocationChecker(store, -1) // disable cache for deterministic tests
	keys := NewStaticKeyResolver(map[string]ed25519.PublicKey{"k1": pub})
	v, err := NewStandardVerifier(VerifierConfig{
		Keys:           keys,
		Revocations:    cache,
		TrustedIssuers: []string{"paladin-test"},
	})
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	return issuer, v, store, pub
}

// TestIssue_VerifyHappyPath issues a token and verifies it under the
// matching audience.
func TestIssue_VerifyHappyPath(t *testing.T) {
	t.Parallel()
	issuer, verifier, _, _ := buildIssuerVerifier(t)

	ctx := context.Background()
	cap, token, err := issuer.Issue(ctx, IssueRequest{
		IssuedBy: Principal{Subject: "test-operator"},
		Subject: Principal{
			Type:     PrincipalAgent,
			TenantID: uuid.New(),
			Subject:  "agent-1",
		},
		Audience: []string{AudiencePlaneData},
		Caveats:  Caveats{Ops: []Op{OpGet, OpList}},
		TTL:      time.Minute,
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	got, err := verifier.Verify(ctx, token, AudiencePlaneData)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.ID != cap.ID {
		t.Errorf("verified ID mismatch")
	}
}

// TestVerify_AudienceMismatch confirms tokens issued for one plane fail
// verification on a different plane.
func TestVerify_AudienceMismatch(t *testing.T) {
	t.Parallel()
	issuer, verifier, _, _ := buildIssuerVerifier(t)
	ctx := context.Background()
	_, token, _ := issuer.Issue(ctx, IssueRequest{
		IssuedBy: Principal{Subject: "test-operator"},
		Subject:  Principal{Type: PrincipalAgent, TenantID: uuid.New()},
		Audience: []string{AudiencePlaneData},
		Caveats:  Caveats{Ops: []Op{OpGet}},
	})
	_, err := verifier.Verify(ctx, token, AudiencePlaneAdmin)
	if !errors.Is(err, ErrAudienceMismatch) {
		t.Fatalf("expected ErrAudienceMismatch, got %v", err)
	}
}

// TestVerify_Expired confirms tokens past ExpiresAt fail.
func TestVerify_Expired(t *testing.T) {
	t.Parallel()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := NewEd25519Signer("k1", priv)
	store := newMemStore()
	cache := NewCachedRevocationChecker(store, -1)
	keys := NewStaticKeyResolver(map[string]ed25519.PublicKey{"k1": pub})

	// Inject a clock that returns a fixed past time at issue, then
	// jumps an hour ahead at verify.
	frozen := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: frozen}

	issuer, _ := NewIssuer(IssuerConfig{
		Signer:     signer,
		Store:      store,
		IssuerName: "paladin-test",
		Now:        clock.Now,
		DefaultTTL: time.Minute,
	})
	verifier, _ := NewStandardVerifier(VerifierConfig{
		Keys:           keys,
		Revocations:    cache,
		TrustedIssuers: []string{"paladin-test"},
		Now:            clock.Now,
		Leeway:         time.Second,
	})

	ctx := context.Background()
	_, token, _ := issuer.Issue(ctx, IssueRequest{
		IssuedBy: Principal{Subject: "test-operator"},
		Subject:  Principal{Type: PrincipalAgent, TenantID: uuid.New()},
		Audience: []string{AudiencePlaneData},
		Caveats:  Caveats{Ops: []Op{OpGet}},
	})

	clock.now = frozen.Add(time.Hour) // jump past expiry
	_, err := verifier.Verify(ctx, token, AudiencePlaneData)
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("expected ErrExpired, got %v", err)
	}
}

// TestVerify_Revoked confirms the revocation gate.
func TestVerify_Revoked(t *testing.T) {
	t.Parallel()
	issuer, verifier, store, _ := buildIssuerVerifier(t)
	ctx := context.Background()
	cap, token, _ := issuer.Issue(ctx, IssueRequest{
		IssuedBy: Principal{Subject: "test-operator"},
		Subject:  Principal{Type: PrincipalAgent, TenantID: uuid.New()},
		Audience: []string{AudiencePlaneData},
		Caveats:  Caveats{Ops: []Op{OpGet}},
	})
	if err := store.Revoke(ctx, RevokeRequest{ID: cap.ID, Reason: "test"}); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	_, err := verifier.Verify(ctx, token, AudiencePlaneData)
	if !errors.Is(err, ErrRevoked) {
		t.Fatalf("expected ErrRevoked, got %v", err)
	}
}

// TestVerify_UntrustedIssuer rejects tokens whose `iss` is outside the
// trusted set even when the signature checks out.
func TestVerify_UntrustedIssuer(t *testing.T) {
	t.Parallel()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := NewEd25519Signer("k1", priv)
	store := newMemStore()

	rogueIssuer, _ := NewIssuer(IssuerConfig{
		Signer:     signer,
		Store:      store,
		IssuerName: "paladin-rogue",
	})
	_, token, _ := rogueIssuer.Issue(context.Background(), IssueRequest{
		IssuedBy: Principal{Subject: "test-operator"},
		Subject:  Principal{Type: PrincipalAgent, TenantID: uuid.New()},
		Audience: []string{AudiencePlaneData},
		Caveats:  Caveats{Ops: []Op{OpGet}},
	})

	cache := NewCachedRevocationChecker(store, -1)
	keys := NewStaticKeyResolver(map[string]ed25519.PublicKey{"k1": pub})
	verifier, _ := NewStandardVerifier(VerifierConfig{
		Keys:           keys,
		Revocations:    cache,
		TrustedIssuers: []string{"paladin-test"}, // rogue not in set
	})
	_, err := verifier.Verify(context.Background(), token, AudiencePlaneData)
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature, got %v", err)
	}
}

// TestDelegate_NarrowsAndPersists confirms a delegated child is both
// narrower (Narrows passes) and recorded with parent_id.
func TestDelegate_NarrowsAndPersists(t *testing.T) {
	t.Parallel()
	issuer, verifier, store, _ := buildIssuerVerifier(t)
	ctx := context.Background()
	tenantID := uuid.New()

	parent, _, err := issuer.Issue(ctx, IssueRequest{
		IssuedBy: Principal{Subject: "test-operator"},
		Subject:  Principal{Type: PrincipalAgent, TenantID: tenantID},
		Audience: []string{AudiencePlaneData, AudiencePlaneMCP},
		Caveats: Caveats{
			Ops:              []Op{OpGet, OpList, OpSearch},
			ResourcePrefixes: []string{"object://acme/"},
			MaxBudgetAmount:  MustParseAmount("1.00"),
		},
		TTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("issue parent: %v", err)
	}

	child, childToken, err := issuer.Delegate(ctx, DelegateRequest{
		Parent:   parent,
		Subject:  Principal{Type: PrincipalAgent, TenantID: tenantID, Subject: "child"},
		Audience: []string{AudiencePlaneData},
		Caveats: Caveats{
			Ops:              []Op{OpGet},
			ResourcePrefixes: []string{"object://acme/run-42/"},
			MaxBudgetAmount:  NanosPerUnit / 10,
		},
		TTL: 15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("delegate: %v", err)
	}
	if child.ParentID != parent.ID {
		t.Errorf("child parent_id mismatch")
	}
	if _, ok := store.caps[child.ID]; !ok {
		t.Errorf("child not persisted")
	}
	if _, err := verifier.Verify(ctx, childToken, AudiencePlaneData); err != nil {
		t.Fatalf("verify child: %v", err)
	}
}

// TestDelegate_RejectsWidening confirms the issuer enforces narrowing
// at issuance — a misuse fails before any token is emitted.
func TestDelegate_RejectsWidening(t *testing.T) {
	t.Parallel()
	issuer, _, _, _ := buildIssuerVerifier(t)
	ctx := context.Background()
	tenantID := uuid.New()
	parent, _, _ := issuer.Issue(ctx, IssueRequest{
		IssuedBy: Principal{Subject: "test-operator"},
		Subject:  Principal{Type: PrincipalAgent, TenantID: tenantID},
		Audience: []string{AudiencePlaneData},
		Caveats:  Caveats{Ops: []Op{OpGet}},
		TTL:      time.Hour,
	})

	_, _, err := issuer.Delegate(ctx, DelegateRequest{
		Parent:   parent,
		Subject:  Principal{Type: PrincipalAgent, TenantID: tenantID},
		Audience: []string{AudiencePlaneData, AudiencePlaneAdmin}, // wider!
		Caveats:  Caveats{Ops: []Op{OpGet}},
	})
	if !errors.Is(err, ErrDelegationTooWide) {
		t.Fatalf("expected ErrDelegationTooWide, got %v", err)
	}
}

// A Biscuit copy with limits of its own cannot delegate: the child would
// count against the capability and never the copy, shedding its limits.
// A copy without limits of its own delegates as the capability does.
func TestDelegate_RefusesACopyWithLimitsOfItsOwn(t *testing.T) {
	t.Parallel()
	issuer, _, store, _ := buildIssuerVerifier(t)
	ctx := context.Background()
	tenantID := uuid.New()
	parent, _, err := issuer.Issue(ctx, IssueRequest{
		IssuedBy: Principal{Subject: "test-operator"},
		Subject:  Principal{Type: PrincipalAgent, TenantID: tenantID},
		Audience: []string{AudiencePlaneData},
		Caveats:  Caveats{Ops: []Op{OpGet}, MaxRequests: 100},
		TTL:      time.Hour,
	})
	if err != nil {
		t.Fatalf("issue parent: %v", err)
	}
	delegate := func(from Capability) error {
		_, _, err := issuer.Delegate(ctx, DelegateRequest{
			Parent:         from,
			Subject:        Principal{Type: PrincipalAgent, TenantID: tenantID, Subject: "child"},
			InheritCaveats: true,
		})
		return err
	}

	copyOf := parent
	copyOf.Copies = []CopyCeiling{{RevocationID: []byte("block"), MaxRequests: 5}}
	before := len(store.caps)
	if err := delegate(copyOf); !errors.Is(err, ErrDelegationTooWide) {
		t.Fatalf("delegate from a copy with limits: err = %v, want ErrDelegationTooWide", err)
	}
	if len(store.caps) != before {
		t.Error("a refused delegation persisted a child")
	}
	if err := delegate(parent); err != nil {
		t.Errorf("delegate from the capability itself: %v", err)
	}
}

// TestCache_HitsAndMisses confirms the revocation cache short-circuits
// repeat lookups within the TTL window.
func TestCache_HitsAndMisses(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	cache := NewCachedRevocationChecker(store, time.Second)
	id := uuid.New()
	ctx := context.Background()

	// First call → miss → upstream.
	_, _ = cache.IsRevoked(ctx, id)
	if got := atomic.LoadInt64(&store.revCalls); got != 1 {
		t.Fatalf("expected 1 upstream call, got %d", got)
	}

	// Second call within TTL → hit, no upstream.
	_, _ = cache.IsRevoked(ctx, id)
	if got := atomic.LoadInt64(&store.revCalls); got != 1 {
		t.Fatalf("expected still 1 upstream call after cache hit, got %d", got)
	}

	// Invalidate forces a refetch.
	cache.Invalidate(id)
	_, _ = cache.IsRevoked(ctx, id)
	if got := atomic.LoadInt64(&store.revCalls); got != 2 {
		t.Fatalf("expected 2 upstream calls after invalidate, got %d", got)
	}
}

// mutableClock is a clock that advances when its `now` field is set.
type mutableClock struct {
	now time.Time
}

func (m *mutableClock) Now() time.Time { return m.now }
