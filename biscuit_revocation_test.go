package limes

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// revokeCopy revokes token's copy through the store the fixture verifier reads.
func revokeCopy(t *testing.T, v *StandardVerifier, store *memStore, token string) BiscuitCopy {
	t.Helper()
	c, err := v.BiscuitCopy(context.Background(), token)
	if err != nil {
		t.Fatalf("BiscuitCopy: %v", err)
	}
	if err := store.RevokeBiscuit(context.Background(), RevokeBiscuitRequest{
		CapabilityID: c.CapabilityID, RevocationID: c.RevocationID, Reason: "test",
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

// Revoking one copy stops it and every copy attenuated from it, and nothing
// above or beside it.
func TestRevokingABiscuitCopy(t *testing.T) {
	_, v, store, cap, root := biscuitFixture(t)
	ctx := context.Background()
	mid, _ := Attenuate(root, Attenuation{Ops: []Op{OpGet, OpList}})
	leaf, _ := Attenuate(mid, Attenuation{Ops: []Op{OpGet}})
	sibling, _ := Attenuate(root, Attenuation{Ops: []Op{OpPut}})

	got := revokeCopy(t, v, store, mid)
	if got.CapabilityID != cap.ID {
		t.Fatalf("copy names capability %s, want %s", got.CapabilityID, cap.ID)
	}

	for name, tc := range map[string]struct {
		token string
		want  error
	}{
		"the revoked copy":           {mid, ErrRevoked},
		"a copy attenuated from it":  {leaf, ErrRevoked},
		"the copy it came from":      {root, nil},
		"a sibling attenuated apart": {sibling, nil},
	} {
		if _, err := v.Verify(ctx, tc.token, AudiencePlaneData); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
}

// An unattenuated Biscuit's last id is its authority block's: revoking it
// stops that Biscuit and every copy attenuated from it, but not the
// capability's JWT.
func TestRevokingTheRootBiscuitLeavesTheJWT(t *testing.T) {
	issuer, v, store, _, _ := biscuitFixture(t)
	ctx := context.Background()
	cap, jwt, err := issuer.Issue(ctx, IssueRequest{
		IssuedBy: Principal{Subject: "op"},
		Subject:  Principal{TenantID: uuid.New(), Subject: "agent"},
		Audience: []string{AudiencePlaneData},
		Caveats:  Caveats{Ops: []Op{OpGet}},
		TTL:      time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	root, err := issuer.Biscuit(cap)
	if err != nil {
		t.Fatal(err)
	}
	narrowed, _ := Attenuate(root, Attenuation{Planes: []string{AudiencePlaneData}})

	revokeCopy(t, v, store, root)

	if _, err := v.Verify(ctx, narrowed, AudiencePlaneData); !errors.Is(err, ErrRevoked) {
		t.Errorf("attenuated Biscuit: err = %v, want ErrRevoked", err)
	}
	if _, err := v.Verify(ctx, jwt, AudiencePlaneData); err != nil {
		t.Errorf("JWT: %v, want it still valid", err)
	}
}

// The id BiscuitCopy names is the copy's own last block, and it survives a
// round trip through serialisation: the id revoked is the id presented.
func TestBiscuitCopyNamesTheLastBlock(t *testing.T) {
	_, v, _, _, root := biscuitFixture(t)
	ctx := context.Background()
	mid, _ := Attenuate(root, Attenuation{Ops: []Op{OpGet}})

	rootCopy, err := v.BiscuitCopy(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	midCopy, err := v.BiscuitCopy(ctx, mid)
	if err != nil {
		t.Fatal(err)
	}
	b, err := parseBiscuit(mid)
	if err != nil {
		t.Fatal(err)
	}
	ids := b.RevocationIds()
	if len(ids) != 2 || !bytes.Equal(ids[0], rootCopy.RevocationID) || !bytes.Equal(ids[1], midCopy.RevocationID) {
		t.Fatalf("ids %x; root copy %x, mid copy %x", ids, rootCopy.RevocationID, midCopy.RevocationID)
	}
}

// BiscuitCopy checks what Verify checks about the token itself, so nobody
// records a revocation against a capability the token does not carry.
func TestBiscuitCopyRefusesWhatVerifyRefuses(t *testing.T) {
	issuer, v, _, cap, root := biscuitFixture(t)
	ctx := context.Background()
	_, jwt, err := issuer.Issue(ctx, IssueRequest{
		IssuedBy: Principal{Subject: "op"},
		Subject:  Principal{TenantID: uuid.New(), Subject: "agent"},
		Audience: []string{AudiencePlaneData},
		Caveats:  Caveats{Ops: []Op{OpGet}},
		TTL:      time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(root)
	raw[len(raw)-3] ^= 1
	tampered := base64.RawURLEncoding.EncodeToString(raw)

	stranger, _, _, _ := buildIssuerVerifier(t) // another key, same issuer name
	foreign, err := stranger.Biscuit(cap)
	if err != nil {
		t.Fatal(err)
	}

	for name, token := range map[string]string{
		"a JWT":              jwt,
		"a tampered Biscuit": tampered,
		"an untrusted key":   foreign,
		"garbage":            "AAAA",
		"too long":           string(bytes.Repeat([]byte("A"), DefaultMaxTokenBytes+1)),
	} {
		if _, err := v.BiscuitCopy(ctx, token); !errors.Is(err, ErrInvalidSignature) {
			t.Errorf("%s: err = %v, want ErrInvalidSignature", name, err)
		}
	}
}

// A copy can be revoked whatever its state: expired, or its capability
// already revoked.
func TestBiscuitCopyIgnoresExpiryAndRevocation(t *testing.T) {
	_, v, store, cap, root := biscuitFixture(t)
	ctx := context.Background()
	if err := store.Revoke(ctx, RevokeRequest{ID: cap.ID}); err != nil {
		t.Fatal(err)
	}
	v.cfg.Now = func() time.Time { return cap.ExpiresAt.Add(24 * time.Hour) }
	if _, err := v.BiscuitCopy(ctx, root); err != nil {
		t.Fatalf("BiscuitCopy: %v", err)
	}
}

// The shared attenuated token is refused once the copy it was attenuated from
// is revoked: it carries the same ids the server lists.
func TestSharedAttenuationRevokedThroughItsSeed(t *testing.T) {
	ctx := context.Background()
	copies := map[string]bool{}
	v := goldenBiscuitVerifier(t)
	v.cfg.BiscuitRevocations = biscuitLookupFunc(func(ids [][]byte) bool {
		for _, id := range ids {
			if copies[string(id)] {
				return true
			}
		}
		return false
	})
	seed := readBiscuitFixture(t, biscuitSeedFile)
	attenuated := readBiscuitFixture(t, biscuitAttenuatedFile)
	seedCopy, err := v.BiscuitCopy(ctx, seed)
	if err != nil {
		t.Fatal(err)
	}
	attCopy, err := v.BiscuitCopy(ctx, attenuated)
	if err != nil {
		t.Fatal(err)
	}
	if attCopy.CapabilityID != seedCopy.CapabilityID || bytes.Equal(attCopy.RevocationID, seedCopy.RevocationID) {
		t.Fatalf("attenuated copy %+v, seed copy %+v", attCopy, seedCopy)
	}
	copies[string(seedCopy.RevocationID)] = true
	if _, err := v.Verify(ctx, attenuated, AudiencePlaneData); !errors.Is(err, ErrRevoked) {
		t.Fatalf("err = %v, want ErrRevoked", err)
	}
}

type biscuitLookupFunc func([][]byte) bool

func (f biscuitLookupFunc) IsBiscuitRevoked(_ context.Context, ids [][]byte) (bool, error) {
	return f(ids), nil
}

type failingBiscuitLookup struct{ err error }

func (f failingBiscuitLookup) IsBiscuitRevoked(context.Context, [][]byte) (bool, error) {
	return false, f.err
}

// A lookup that fails refuses the token rather than passing it.
func TestBiscuitRevocationLookupFailsClosed(t *testing.T) {
	_, v, _, _, root := biscuitFixture(t)
	boom := errors.New("database down")
	v.cfg.BiscuitRevocations = failingBiscuitLookup{boom}
	if _, err := v.Verify(context.Background(), root, AudiencePlaneData); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the lookup's error", err)
	}
}

// Accepting Biscuits with nothing to check their copies against is a wiring
// mistake, refused at construction.
func TestVerifierNeedsBiscuitRevocationsToAcceptBiscuits(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	cfg := VerifierConfig{
		Keys:           NewStaticKeyResolver(map[string]ed25519.PublicKey{"k1": pub}),
		Revocations:    revLookup{},
		TrustedIssuers: []string{"limes-test"},
		AcceptBiscuit:  true,
	}
	if _, err := NewStandardVerifier(cfg); !errors.Is(err, errNoBiscuitRevocations) {
		t.Fatalf("err = %v, want errNoBiscuitRevocations", err)
	}
	cfg.AcceptBiscuit = false
	if _, err := NewStandardVerifier(cfg); err != nil {
		t.Fatalf("without Biscuits: %v", err)
	}
}

type countingBiscuitLookup struct {
	calls   int
	revoked bool
	err     error
}

func (c *countingBiscuitLookup) IsBiscuitRevoked(context.Context, [][]byte) (bool, error) {
	c.calls++
	return c.revoked, c.err
}

func TestCachedBiscuitRevocationChecker(t *testing.T) {
	ctx := context.Background()
	ttl := time.Second
	ids := [][]byte{[]byte("ab"), []byte("c")}

	t.Run("serves a fresh answer, then asks again once it expires", func(t *testing.T) {
		now := time.Unix(0, 0)
		up := &countingBiscuitLookup{revoked: true}
		c := NewCachedBiscuitRevocationChecker(up, ttl, WithCacheClock(func() time.Time { return now }))
		for range 3 {
			if got, _ := c.IsBiscuitRevoked(ctx, ids); !got {
				t.Fatal("want revoked")
			}
		}
		if up.calls != 1 {
			t.Fatalf("upstream calls = %d, want 1", up.calls)
		}
		now = now.Add(ttl)
		_, _ = c.IsBiscuitRevoked(ctx, ids)
		if up.calls != 2 {
			t.Fatalf("upstream calls after expiry = %d, want 2", up.calls)
		}
	})

	t.Run("Clear sends the next check upstream", func(t *testing.T) {
		up := &countingBiscuitLookup{}
		c := NewCachedBiscuitRevocationChecker(up, ttl)
		_, _ = c.IsBiscuitRevoked(ctx, ids)
		c.Clear()
		_, _ = c.IsBiscuitRevoked(ctx, ids)
		if up.calls != 2 {
			t.Fatalf("upstream calls = %d, want 2", up.calls)
		}
	})

	t.Run("an error is returned and not cached", func(t *testing.T) {
		boom := errors.New("down")
		up := &countingBiscuitLookup{err: boom}
		c := NewCachedBiscuitRevocationChecker(up, ttl)
		if _, err := c.IsBiscuitRevoked(ctx, ids); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		up.err = nil
		_, _ = c.IsBiscuitRevoked(ctx, ids)
		if up.calls != 2 {
			t.Fatalf("upstream calls = %d, want 2", up.calls)
		}
	})

	t.Run("lists that concatenate alike are different tokens", func(t *testing.T) {
		up := &countingBiscuitLookup{}
		c := NewCachedBiscuitRevocationChecker(up, ttl)
		_, _ = c.IsBiscuitRevoked(ctx, ids)
		_, _ = c.IsBiscuitRevoked(ctx, [][]byte{[]byte("a"), []byte("bc")})
		if up.calls != 2 {
			t.Fatalf("upstream calls = %d, want 2", up.calls)
		}
	})

	t.Run("stays within its bound", func(t *testing.T) {
		const bound = 2
		up := &countingBiscuitLookup{}
		c := NewCachedBiscuitRevocationChecker(up, ttl, WithMaxEntries(bound))
		for i := range bound * 3 {
			_, _ = c.IsBiscuitRevoked(ctx, [][]byte{{byte(i)}})
			if len(c.cache.entries) > bound {
				t.Fatalf("%d entries, bound %d", len(c.cache.entries), bound)
			}
		}
	})

	t.Run("a negative ttl disables caching", func(t *testing.T) {
		up := &countingBiscuitLookup{}
		c := NewCachedBiscuitRevocationChecker(up, -1)
		_, _ = c.IsBiscuitRevoked(ctx, ids)
		_, _ = c.IsBiscuitRevoked(ctx, ids)
		if up.calls != 2 {
			t.Fatalf("upstream calls = %d, want 2", up.calls)
		}
	})
}
