package inspect_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/inspect"
	"github.com/oleg-tkachuk/limes/memstore"
)

// issue mints a capability and returns it, its token and the issuer's key.
func issue(t *testing.T) (limes.Capability, string, ed25519.PublicKey) {
	t.Helper()
	key, err := limes.GenerateKey()
	kid, pub, priv := key.KeyID, key.Public, key.Private
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
	c, token, err := issuer.Issue(context.Background(), limes.IssueRequest{
		IssuedBy: limes.Principal{Subject: "op"},
		Subject:  limes.Principal{Type: limes.PrincipalAgent, TenantID: uuid.New(), Subject: "agent"},
		Audience: []string{"data"},
		Caveats:  limes.Caveats{Ops: []limes.Op{limes.OpGet}},
		TTL:      time.Minute,
	})
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
	if _, err := inspect.Decode("not.a.token"); err == nil {
		t.Error("Decode read a token that is not one")
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
		"a short key": {token, pub[:8]},
	} {
		if err := inspect.VerifySignature(tc.token, tc.key); !errors.Is(err, limes.ErrInvalidToken) {
			t.Errorf("%s: err = %v, want ErrInvalidToken", name, err)
		}
	}
}
