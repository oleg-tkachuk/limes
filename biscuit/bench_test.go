package biscuit_test

// What the Biscuit form costs beside the JWT it seals: sealing a
// capability, attenuating it, and verifying it with and without blocks.

import (
	"context"
	"strconv"
	"testing"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/biscuit"
)

// benchBlocks is how many attenuation blocks the attenuated Biscuit carries.
const benchBlocks = 3

// benchSealed is the Biscuit form of a capability of s, attenuated by
// blocks blocks, each a prefix under the one before.
func benchSealed(b *testing.B, s exampleStack, blocks int) string {
	b.Helper()
	_, token := s.issue()
	res, err := s.issuer.Biscuit(context.Background(), limes.BiscuitRequest{Capability: s.verified(token)})
	if err != nil {
		b.Fatal(err)
	}
	sealed, prefix := res.Token, "corpus/public/"
	for i := range blocks {
		prefix += strconv.Itoa(i) + "/"
		if sealed, err = biscuit.Attenuate(sealed, limes.Attenuation{ResourcePrefixes: []string{prefix}}); err != nil {
			b.Fatal(err)
		}
	}
	return sealed
}

// Minting the Biscuit form of a capability the verifier accepted.
func BenchmarkBiscuit(b *testing.B) {
	s := newExampleStack()
	_, token := s.issue()
	req := limes.BiscuitRequest{Capability: s.verified(token)}
	for b.Loop() {
		if _, err := s.issuer.Biscuit(context.Background(), req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkVerifyBiscuit(b *testing.B) {
	s := newExampleStack()
	req := limes.VerifyRequest{Token: benchSealed(b, s, 0), Audience: exampleAudience}
	for b.Loop() {
		if _, err := s.verifier.Verify(context.Background(), req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkVerifyBiscuitAttenuated(b *testing.B) {
	s := newExampleStack()
	req := limes.VerifyRequest{Token: benchSealed(b, s, benchBlocks), Audience: exampleAudience}
	for b.Loop() {
		if _, err := s.verifier.Verify(context.Background(), req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAttenuate(b *testing.B) {
	s := newExampleStack()
	sealed := benchSealed(b, s, 0)
	a := limes.Attenuation{ResourcePrefixes: []string{"corpus/public/2026/"}}
	for b.Loop() {
		if _, err := biscuit.Attenuate(sealed, a); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkVerifyBiscuitParallel(b *testing.B) {
	s := newExampleStack()
	req := limes.VerifyRequest{Token: benchSealed(b, s, benchBlocks), Audience: exampleAudience}
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := s.verifier.Verify(context.Background(), req); err != nil {
				b.Error(err)
				return
			}
		}
	})
}
