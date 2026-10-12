package limes

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"math"
	"testing"

	"github.com/biscuit-auth/biscuit-go/v2/datalog"
	"github.com/biscuit-auth/biscuit-go/v2/pb"
	"google.golang.org/protobuf/proto"
)

// A Biscuit is read from bytes its holder — or anyone — controls, and part of
// it before any signature is checked. Whatever those bytes say, the reader
// refuses them; it never panics.

// forgedBiscuit is a Biscuit whose one block is blk, with a structurally
// complete but unsigned chain: what an attacker without any key can send.
func forgedBiscuit(t *testing.T, blk *pb.Block) string {
	t.Helper()
	return forgedBiscuitToken(t, blk, make([]byte, ed25519.SeedSize))
}

// forgedBiscuitToken is forgedBiscuit with secret as its proof's next secret.
func forgedBiscuitToken(t testing.TB, blk *pb.Block, secret []byte) string {
	t.Helper()
	raw, err := proto.Marshal(blk)
	if err != nil {
		t.Fatal(err)
	}
	alg := pb.PublicKey_Ed25519
	c := &pb.Biscuit{
		Authority: &pb.SignedBlock{
			Block:     raw,
			NextKey:   &pb.PublicKey{Algorithm: &alg, Key: make([]byte, ed25519.PublicKeySize)},
			Signature: make([]byte, ed25519.SignatureSize),
		},
		Proof: &pb.Proof{Content: &pb.Proof_NextSecret{NextSecret: secret}},
	}
	out, err := proto.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(out)
}

// factBlock is a block of one fact, its name and string term given as
// symbol ids.
func factBlock(name uint64, term *pb.TermV2) *pb.Block {
	return &pb.Block{FactsV2: []*pb.FactV2{{Predicate: &pb.PredicateV2{Name: &name, Terms: []*pb.TermV2{term}}}}}
}

func stringTerm(id uint64) *pb.TermV2 { return &pb.TermV2{Content: &pb.TermV2_String_{String_: id}} }

func integerTerm() *pb.TermV2 { return &pb.TermV2{Content: &pb.TermV2_Integer{Integer: 1}} }

// verifyNoPanic is v.Verify of token, failing the test if it panics.
func verifyNoPanic(t *testing.T, v *Verifier, token string) (err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Verify panicked: %v", r)
		}
	}()
	_, err = v.Verify(context.Background(), VerifyRequest{Token: token, Audience: audienceData})
	return err
}

func TestBiscuitRefusesASymbolOutsideBothTables(t *testing.T) {
	_, v, _, _, _ := biscuitFixture(t)
	defaults := uint64(len(datalog.DEFAULT_SYMBOLS))
	table := uint64(datalog.OFFSET)
	ids := map[string]uint64{
		"past the default symbols":   defaults,
		"just below the block table": table - 1,
		"past the block table":       table,
		"with the top bit set":       1 << 63,
		"the largest id":             math.MaxUint64,
	}
	for name, id := range ids {
		t.Run("name "+name, func(t *testing.T) {
			if err := verifyNoPanic(t, v, forgedBiscuit(t, factBlock(id, integerTerm()))); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("err = %v, want ErrInvalidToken", err)
			}
		})
		t.Run("string term "+name, func(t *testing.T) {
			blk := factBlock(table, stringTerm(id))
			blk.Symbols = []string{biscuitFactCapability}
			if err := verifyNoPanic(t, v, forgedBiscuit(t, blk)); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("err = %v, want ErrInvalidToken", err)
			}
		})
	}
}

// withBiscuitProof is token with its proof replaced by p.
func withBiscuitProof(t *testing.T, token string, p *pb.Proof) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatal(err)
	}
	var c pb.Biscuit
	if err := proto.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	c.Proof = p
	out, err := proto.Marshal(&c)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(out)
}

// A genuine Biscuit with its proof cut short is refused by every reader of a
// Biscuit — Verify, BiscuitCopy and Attenuate — rather than handed to a
// library that would panic on it.
func TestBiscuitRefusesAMalformedProof(t *testing.T) {
	issuer, v, _, cap, _ := biscuitFixture(t)
	token, err := issuer.biscuit(context.Background(), asVerified(cap))
	if err != nil {
		t.Fatal(err)
	}
	proofs := map[string]*pb.Proof{
		"a short next secret":     {Content: &pb.Proof_NextSecret{NextSecret: []byte{1}}},
		"a long next secret":      {Content: &pb.Proof_NextSecret{NextSecret: make([]byte, ed25519.SeedSize+1)}},
		"a short final signature": {Content: &pb.Proof_FinalSignature{FinalSignature: []byte{1}}},
		"no content":              {},
	}
	for name, p := range proofs {
		t.Run(name, func(t *testing.T) {
			bad := withBiscuitProof(t, token, p)
			if err := verifyNoPanic(t, v, bad); !errors.Is(err, ErrInvalidToken) {
				t.Errorf("Verify: err = %v, want ErrInvalidToken", err)
			}
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("BiscuitCopy or Attenuate panicked: %v", r)
					}
				}()
				if _, err := v.BiscuitCopy(context.Background(), bad); !errors.Is(err, ErrInvalidToken) {
					t.Errorf("BiscuitCopy: err = %v, want ErrInvalidToken", err)
				}
				if _, err := Attenuate(bad, Attenuation{Ops: []Op{OpGet}}); !errors.Is(err, ErrInvalidToken) {
					t.Errorf("Attenuate: err = %v, want ErrInvalidToken", err)
				}
			}()
		})
	}
}

// A chain stops at MaxBiscuitBlocks: Attenuate will not append past it, and
// a verifier refuses a token that has, before looking any of its ids up.
func TestBiscuitBlocksAreBounded(t *testing.T) {
	_, v, _, _, token := biscuitFixture(t)
	for i := range MaxBiscuitBlocks {
		next, err := Attenuate(token, Attenuation{Ops: []Op{OpGet}})
		if err != nil {
			t.Fatalf("block %d of %d: %v", i+1, MaxBiscuitBlocks, err)
		}
		token = next
	}
	if _, err := v.Verify(context.Background(), VerifyRequest{Token: token, Audience: audienceData}); err != nil {
		t.Fatalf("a token of MaxBiscuitBlocks blocks: %v", err)
	}
	if _, err := Attenuate(token, Attenuation{Ops: []Op{OpGet}}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("a block past MaxBiscuitBlocks: err = %v, want ErrInvalidRequest", err)
	}
	// One more block, appended as any holder could without Attenuate.
	b, err := parseBiscuit(token)
	if err != nil {
		t.Fatal(err)
	}
	next, err := b.Append(rand.Reader, b.CreateBlock().Build())
	if err != nil {
		t.Fatal(err)
	}
	over, err := serializeBiscuit(next)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(context.Background(), VerifyRequest{Token: over, Audience: audienceData}); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("a token past MaxBiscuitBlocks: err = %v, want ErrInvalidToken", err)
	}
}
