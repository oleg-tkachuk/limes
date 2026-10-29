package biscuit

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	biscuitgo "github.com/eclipse-biscuit/biscuit-go/v2"
	"github.com/eclipse-biscuit/biscuit-go/v2/pb"

	"github.com/oleg-tkachuk/limes"
)

// appendBlock appends to token a block blk builds, as a holder with
// biscuit-go and no regard for the vocabulary could.
func appendBlock(t *testing.T, token string, build func(*testing.T, biscuitgo.BlockBuilder)) string {
	t.Helper()
	b, err := parseBiscuit(token)
	if err != nil {
		t.Fatal(err)
	}
	blk := b.CreateBlock()
	build(t, blk)
	next, err := b.Append(rand.Reader, blk.Build())
	if err != nil {
		t.Fatal(err)
	}
	out, err := serializeBiscuit(next)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A block holding a datalog check, or a fact outside the vocabulary, is
// refused: a restriction the verifier cannot enforce must not pass as one it
// did.
func TestBiscuitRefusesWhatItCannotEnforce(t *testing.T) {
	s, _, token := biscuitFixture(t)
	withCheck := appendBlock(t, token, func(t *testing.T, blk biscuitgo.BlockBuilder) {
		if err := blk.AddCheck(biscuitgo.Check{Queries: []biscuitgo.Rule{{
			Head: biscuitgo.Predicate{Name: "q", IDs: []biscuitgo.Term{biscuitgo.Variable("x")}},
			Body: []biscuitgo.Predicate{{Name: "operation", IDs: []biscuitgo.Term{biscuitgo.Variable("x")}}},
		}}}); err != nil {
			t.Fatal(err)
		}
	})
	withFact := appendBlock(t, token, func(t *testing.T, blk biscuitgo.BlockBuilder) {
		if err := blk.AddFact(biscuitFact("right", biscuitgo.String("everything"))); err != nil {
			t.Fatal(err)
		}
	})
	for name, tok := range map[string]string{"a datalog check": withCheck, "a foreign fact": withFact} {
		if _, err := s.verifier.Verify(context.Background(), limes.VerifyRequest{Token: tok, Audience: audienceData}); !errors.Is(err, limes.ErrBiscuitAttenuation) {
			t.Errorf("%s: err = %v, want ErrBiscuitAttenuation", name, err)
		}
	}
}

// The signature chain holds: a Biscuit re-wrapping the sealed JWT under a
// root of the holder's own is refused, as is one around a JWT that names no
// root, and one with any byte changed.
func TestBiscuitCannotBeStrippedOrForged(t *testing.T) {
	s, cap, token := biscuitFixture(t)
	narrowed, err := Attenuate(token, limes.Attenuation{Ops: []limes.Op{limes.OpGet}})
	if err != nil {
		t.Fatal(err)
	}
	inner, _, err := Form().Open(narrowed)
	if err != nil {
		t.Fatal(err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrap := func(sealed string) string {
		bb := biscuitgo.NewBuilder(priv)
		if err := bb.AddAuthorityFact(biscuitFact(biscuitFactCapability, biscuitgo.String(sealed))); err != nil {
			t.Fatal(err)
		}
		b, err := bb.Build()
		if err != nil {
			t.Fatal(err)
		}
		out, err := serializeBiscuit(b)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	plain, err := s.issuer.Issue(context.Background(), limes.IssueRequest{ClientID: testClientID,
		IssuedBy: limes.Principal{Subject: "op"}, Subject: cap.Subject, Audience: []string{audienceData},
		Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}, Requests: limes.RequestCeiling{Unlimited: true}}, TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(narrowed)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-3] ^= 1
	for name, tok := range map[string]string{
		"the sealed JWT under another root": wrap(inner),
		"a JWT that names no root":          wrap(plain.Token),
		"a byte changed":                    base64.RawURLEncoding.EncodeToString(raw),
	} {
		if _, err := s.verifier.Verify(context.Background(), limes.VerifyRequest{Token: tok, Audience: audienceData}); !errors.Is(err, limes.ErrInvalidToken) {
			t.Errorf("%s: err = %v, want ErrInvalidToken", name, err)
		}
	}
}

// The shared attenuated token is refused once the copy it was attenuated
// from is revoked: it carries the same ids the server lists.
func TestSharedAttenuationRevokedThroughItsSeed(t *testing.T) {
	ctx := context.Background()
	copies := map[string]bool{}
	v := goldenStack(t, biscuitLookupFunc(func(ids [][]byte) bool {
		for _, id := range ids {
			if copies[string(id)] {
				return true
			}
		}
		return false
	})).verifier
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
	if attCopy.CapabilityID() != seedCopy.CapabilityID() || bytes.Equal(attCopy.RevocationID(), seedCopy.RevocationID()) {
		t.Fatalf("attenuated copy %+v, seed copy %+v", attCopy, seedCopy)
	}
	copies[string(seedCopy.RevocationID())] = true
	req := withProof(t.Context(), t, readBiscuitCase(t).bindKey(t), attenuated, audienceData, goldenClock())
	if _, err := v.Verify(ctx, req); !errors.Is(err, limes.ErrRevoked) {
		t.Fatalf("err = %v, want ErrRevoked", err)
	}
}

// biscuitLookupFunc revokes the copies f names.
type biscuitLookupFunc func([][]byte) bool

func (f biscuitLookupFunc) IsBiscuitRevoked(_ context.Context, ids [][]byte) (bool, error) {
	return f(ids), nil
}

// FuzzOpenBiscuit holds the Biscuit reader to refusing, never panicking on,
// a token it cannot read: both the part read before any signature is
// checked, and the chain check that follows once a sealed token has named a
// root.
func FuzzOpenBiscuit(f *testing.F) {
	f.Add("")
	for _, name := range []string{biscuitSeedFile, biscuitAttenuatedFile} {
		raw, err := os.ReadFile(biscuitFixturePath(name))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(strings.TrimSpace(string(raw)))
	}
	// Two that once panicked: a symbol id with its top bit set, and a proof
	// secret one byte long.
	topBit := uint64(1) << 63
	f.Add(forgedBiscuitToken(f, &pb.Block{FactsV2: []*pb.FactV2{{Predicate: &pb.PredicateV2{
		Name: &topBit, Terms: []*pb.TermV2{{Content: &pb.TermV2_Integer{Integer: 1}}},
	}}}}, make([]byte, ed25519.SeedSize)))
	f.Add(forgedBiscuitToken(f, &pb.Block{}, []byte{1}))
	anyRoot := ed25519.PublicKey(make([]byte, ed25519.PublicKeySize))
	f.Fuzz(func(t *testing.T, token string) {
		_, unseal, err := Form().Open(token)
		if err != nil {
			return
		}
		_, _ = unseal(anyRoot)
	})
}
