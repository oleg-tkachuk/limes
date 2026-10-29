package biscuit

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	biscuitgo "github.com/eclipse-biscuit/biscuit-go/v2"
	"github.com/oleg-tkachuk/limes"
)

// A refusal names its family once: the verifier keeps this form's text,
// which names the family already, rather than prefixing the family again.
// Consumers pin these messages, so the test checks the text, not only the
// sentinel.
func TestBiscuitRefusalNamesItsFamilyOnce(t *testing.T) {
	const notIssued = "not-issued"
	s, _, token := biscuitFixture(t)
	_, b64Err := unB64Biscuit(notIssued)
	if b64Err == nil {
		t.Fatalf("%q decodes as a Biscuit's base64url", notIssued)
	}
	foreign := appendBlock(t, token, func(t *testing.T, blk biscuitgo.BlockBuilder) {
		if err := blk.AddFact(biscuitFact("right", biscuitgo.String("everything"))); err != nil {
			t.Fatal(err)
		}
	})
	cases := map[string]struct {
		token   string
		family  error
		message string
	}{
		"not base64": {
			token:   notIssued,
			family:  limes.ErrInvalidToken,
			message: limes.ErrInvalidToken.Error() + ": biscuit encoding: " + b64Err.Error(),
		},
		"a malformed container": {
			token:   base64.RawURLEncoding.EncodeToString([]byte{0xff, 0xff, 0xff}),
			family:  limes.ErrInvalidToken,
			message: limes.ErrInvalidToken.Error() + ": biscuit container",
		},
		"a block outside the vocabulary": {
			token:   foreign,
			family:  limes.ErrBiscuitAttenuation,
			message: limes.ErrBiscuitAttenuation.Error() + `: block 1: fact "right" is not part of the attenuation vocabulary`,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := s.verifier.Verify(context.Background(), limes.VerifyRequest{Token: c.token, Audience: audienceData})
			if !errors.Is(err, c.family) {
				t.Fatalf("err = %v, want %v", err, c.family)
			}
			if err.Error() != c.message {
				t.Errorf("message = %q, want %q", err.Error(), c.message)
			}
		})
	}
}
