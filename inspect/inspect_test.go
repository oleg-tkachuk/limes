package inspect_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/inspect"
	"github.com/oleg-tkachuk/limes/memstore"
)

// issue mints a capability and returns it, its token and the issuer's key.
// testClientID is the OAuth client the tests' capabilities name.
const testClientID = "test-client"

func issue(t *testing.T) (limes.Capability, string, ed25519.PublicKey) {
	t.Helper()
	key, err := limes.GenerateKey()
	kid, pub, priv := key.KeyID, key.Public(), key.Private
	if err != nil {
		t.Fatal(err)
	}
	signer, err := limes.NewSigner(limes.SignerConfig{KeyID: kid, Key: priv})
	if err != nil {
		t.Fatal(err)
	}
	records, err := memstore.NewStore(memstore.StoreConfig{})
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := limes.NewIssuer(limes.IssuerConfig{Signer: signer, Store: records, IssuerName: "inspect-test"})
	if err != nil {
		t.Fatal(err)
	}
	cResult, err := issuer.Issue(context.Background(), limes.IssueRequest{
		ClientID: testClientID,
		IssuedBy: limes.Principal{Subject: "op"},
		Subject:  limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: uuid.New(), Subject: "agent"},
		Audience: []string{"data"},
		Caveats:  limes.Caveats{Requests: limes.RequestCeiling{Unlimited: true}, Ops: []limes.Op{limes.OpGet}},
		TTL:      time.Minute,
	})
	c, token := cResult.Capability, cResult.Token
	if err != nil {
		t.Fatal(err)
	}
	return c, token, pub
}

func TestDecodeReadsWhatWasIssued(t *testing.T) {
	c, token, _ := issue(t)
	got, err := inspect.Decode(token)
	if err != nil || got.ID != c.ID || got.Subject.Subject != c.Subject.Subject || got.Caveats.Ops[0] != limes.OpGet {
		t.Fatalf("Decode = %+v, %v; want the issued capability %s", got, err, c.ID)
	}
	if _, err := inspect.Decode("not.a.token"); !errors.Is(err, limes.ErrInvalidToken) {
		t.Errorf("Decode of a token that is not one: err = %v, want ErrInvalidToken", err)
	}
}

func TestVerifySignature(t *testing.T) {
	_, token, pub := issue(t)
	if err := inspect.VerifySignature(token, pub); err != nil {
		t.Fatalf("a genuine token: %v", err)
	}
	other, _, _ := ed25519.GenerateKey(nil)
	// A changed character in the middle of the payload: the end of a
	// base64url segment carries bits a decoder ignores.
	mid := len(token) / 2
	swap := byte('A')
	if token[mid] == swap {
		swap = 'B'
	}
	tampered := token[:mid] + string(swap) + token[mid+1:]
	for name, tc := range map[string]struct {
		token string
		key   ed25519.PublicKey
	}{
		"another key": {token, other},
		"tampered":    {tampered, pub},
		"not a token": {"x", pub},
	} {
		if err := inspect.VerifySignature(tc.token, tc.key); !errors.Is(err, limes.ErrInvalidToken) {
			t.Errorf("%s: err = %v, want ErrInvalidToken", name, err)
		}
	}
	// A key that is no Ed25519 public key is the caller's mistake, not the
	// token's: it says nothing about whether the token is genuine.
	for name, key := range map[string]ed25519.PublicKey{"a short key": pub[:8], "no key": nil, "a long key": append(append(ed25519.PublicKey{}, pub...), 0)} {
		if err := inspect.VerifySignature(token, key); !errors.Is(err, inspect.ErrInvalidKey) || errors.Is(err, limes.ErrInvalidToken) {
			t.Errorf("%s: err = %v, want ErrInvalidKey and not ErrInvalidToken", name, err)
		}
	}
}

// A token over MaxTokenBytes is refused unread, by both; one of exactly
// that length is read. The padding sits in the signature, which Decode does
// not read, so only the size can refuse it.
func TestTokensOverTheSizeLimitAreRefused(t *testing.T) {
	_, token, pub := issue(t)
	pad := func(n int) string { return token + strings.Repeat("A", n-len(token)) }
	if _, err := inspect.Decode(pad(inspect.MaxTokenBytes)); err != nil {
		t.Errorf("Decode of a token of exactly MaxTokenBytes: %v", err)
	}
	over := pad(inspect.MaxTokenBytes + 1)
	if _, err := inspect.Decode(over); !errors.Is(err, limes.ErrInvalidToken) {
		t.Errorf("Decode of a token over MaxTokenBytes: err = %v, want ErrInvalidToken", err)
	}
	if err := inspect.VerifySignature(over, pub); !errors.Is(err, limes.ErrInvalidToken) || !strings.Contains(err.Error(), "bytes") {
		t.Errorf("VerifySignature of a token over MaxTokenBytes: err = %v, want ErrInvalidToken for its size", err)
	}
}
