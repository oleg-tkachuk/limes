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
func revokeCopy(t *testing.T, v *Verifier, store *memStore, token string) BiscuitCopy {
	t.Helper()
	c, err := v.BiscuitCopy(context.Background(), token)
	if err != nil {
		t.Fatalf("BiscuitCopy: %v", err)
	}
	if err := store.RevokeBiscuit(context.Background(), RevokeBiscuitRequest{
		CapabilityID: c.CapabilityID, RevocationID: c.RevocationID, Note: "test",
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
		if _, err := v.Verify(ctx, VerifyRequest{Token: tc.token, Audience: audienceData}); !errors.Is(err, tc.want) {
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
		Subject:  Principal{Type: PrincipalAgent, TenantID: uuid.New(), Subject: "agent"},
		Audience: []string{audienceData},
		Caveats:  Caveats{Ops: []Op{OpGet}},
		TTL:      time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	root, err := issuer.Biscuit(context.Background(), asVerified(cap))
	if err != nil {
		t.Fatal(err)
	}
	narrowed, _ := Attenuate(root, Attenuation{Audience: []string{audienceData}})

	revokeCopy(t, v, store, root)

	if _, err := v.Verify(ctx, VerifyRequest{Token: narrowed, Audience: audienceData}); !errors.Is(err, ErrRevoked) {
		t.Errorf("attenuated Biscuit: err = %v, want ErrRevoked", err)
	}
	if _, err := v.Verify(ctx, VerifyRequest{Token: jwt, Audience: audienceData}); err != nil {
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
		Subject:  Principal{Type: PrincipalAgent, TenantID: uuid.New(), Subject: "agent"},
		Audience: []string{audienceData},
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
	foreign, err := stranger.Biscuit(context.Background(), asVerified(cap))
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
		if _, err := v.BiscuitCopy(ctx, token); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%s: err = %v, want ErrInvalidToken", name, err)
		}
	}
}

// A copy can be revoked whatever its state: expired, or its capability
// already revoked.
func TestBiscuitCopyIgnoresExpiryAndRevocation(t *testing.T) {
	_, v, store, cap, root := biscuitFixture(t)
	ctx := context.Background()
	if err := store.Revoke(ctx, RevokeRequest{CapabilityID: cap.ID}); err != nil {
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
	req := withProof(t, readBiscuitCase(t).bindKey(t), attenuated, audienceData, goldenClock())
	if _, err := v.Verify(ctx, req); !errors.Is(err, ErrRevoked) {
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
	if _, err := v.Verify(context.Background(), VerifyRequest{Token: root, Audience: audienceData}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the lookup's error", err)
	}
}

// Accepting Biscuits with nothing to check their copies against is a wiring
// mistake, refused at construction.
func TestVerifierNeedsBiscuitRevocationsToAcceptBiscuits(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	cfg := VerifierConfig{
		Keys:           staticKeys(t, map[string]ed25519.PublicKey{"k1": pub}),
		Revocations:    revLookup{},
		TrustedIssuers: []string{"limes-test"},
		AcceptBiscuit:  true,
	}
	if _, err := NewVerifier(cfg); !errors.Is(err, errNoBiscuitRevocations) {
		t.Fatalf("err = %v, want errNoBiscuitRevocations", err)
	}
	cfg.AcceptBiscuit = false
	if _, err := NewVerifier(cfg); err != nil {
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

// perIDLookup answers for one revocation id at a time, counting the times
// it is asked about each.
type perIDLookup struct {
	revoked map[string]bool
	asked   map[string]int
}

func (p *perIDLookup) IsBiscuitRevoked(_ context.Context, ids [][]byte) (bool, error) {
	if p.asked == nil {
		p.asked = map[string]int{}
	}
	revoked := false
	for _, id := range ids {
		p.asked[string(id)]++
		revoked = revoked || p.revoked[string(id)]
	}
	return revoked, nil
}

func TestCachedBiscuitRevocationChecker(t *testing.T) {
	ctx := context.Background()
	ttl := time.Second
	ids := [][]byte{[]byte("ab"), []byte("c")}

	t.Run("serves a fresh answer, then asks again once it expires", func(t *testing.T) {
		now := time.Unix(0, 0)
		up := &countingBiscuitLookup{revoked: true}
		c := newBiscuitCache(t, BiscuitRevocationCacheConfig{Upstream: up, TTL: ttl, Now: func() time.Time { return now }})
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
		c := newBiscuitCache(t, BiscuitRevocationCacheConfig{Upstream: up, TTL: ttl})
		_, _ = c.IsBiscuitRevoked(ctx, ids)
		c.Clear()
		_, _ = c.IsBiscuitRevoked(ctx, ids)
		if want := 2 * len(ids); up.calls != want {
			t.Fatalf("upstream calls = %d, want %d: each id, twice", up.calls, want)
		}
	})

	t.Run("an error is returned and not cached", func(t *testing.T) {
		boom := errors.New("down")
		up := &countingBiscuitLookup{err: boom}
		c := newBiscuitCache(t, BiscuitRevocationCacheConfig{Upstream: up, TTL: ttl})
		if _, err := c.IsBiscuitRevoked(ctx, ids); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		up.err = nil
		_, _ = c.IsBiscuitRevoked(ctx, ids)
		// The failed first id is asked again, then the second.
		if want := 1 + len(ids); up.calls != want {
			t.Fatalf("upstream calls = %d, want %d", up.calls, want)
		}
	})

	t.Run("lists that concatenate alike share no id", func(t *testing.T) {
		up := &countingBiscuitLookup{}
		c := newBiscuitCache(t, BiscuitRevocationCacheConfig{Upstream: up, TTL: ttl})
		_, _ = c.IsBiscuitRevoked(ctx, ids)
		_, _ = c.IsBiscuitRevoked(ctx, [][]byte{[]byte("a"), []byte("bc")})
		if up.calls != 4 {
			t.Fatalf("upstream calls = %d, want 4", up.calls)
		}
	})

	// A fresh attenuation adds one block to a token: the ids it shares with
	// the copy it came from are answered from the cache, and only its own is
	// asked about. A revoked id stops the walk.
	t.Run("a fresh attenuation asks only about its new block", func(t *testing.T) {
		const copies = 50
		up := &perIDLookup{revoked: map[string]bool{"revoked": true}}
		c := newBiscuitCache(t, BiscuitRevocationCacheConfig{Upstream: up, TTL: ttl})
		authority := []byte("authority")
		for i := range copies {
			if r, err := c.IsBiscuitRevoked(ctx, [][]byte{authority, {byte(i)}}); err != nil || r {
				t.Fatalf("copy %d = %v, %v; want live", i, r, err)
			}
		}
		if up.asked["authority"] != 1 || len(up.asked) != copies+1 {
			t.Errorf("upstream asked %v; want the authority once and each new block once", up.asked)
		}
		if r, err := c.IsBiscuitRevoked(ctx, [][]byte{authority, []byte("revoked"), []byte("after")}); err != nil || !r {
			t.Errorf("a token with a revoked block = %v, %v; want revoked", r, err)
		}
		if up.asked["after"] != 0 {
			t.Error("a block after a revoked one was asked about")
		}
	})

	t.Run("stays within its bound", func(t *testing.T) {
		const bound = 2
		up := &countingBiscuitLookup{}
		c := newBiscuitCache(t, BiscuitRevocationCacheConfig{Upstream: up, TTL: ttl, MaxEntries: bound})
		for i := range bound * 3 {
			_, _ = c.IsBiscuitRevoked(ctx, [][]byte{{byte(i)}})
			if len(c.cache.entries) > bound {
				t.Fatalf("%d entries, bound %d", len(c.cache.entries), bound)
			}
		}
	})

}
