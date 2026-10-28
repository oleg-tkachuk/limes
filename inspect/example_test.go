package inspect_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/inspect"
	"github.com/oleg-tkachuk/limes/memstore"
)

// exampleToken is a capability token for an agent, signed with a fresh key,
// and that key's public half.
func exampleToken() (string, ed25519.PublicKey) {
	key, err := limes.GenerateKey()
	if err != nil {
		log.Fatal(err)
	}
	records, err := memstore.NewStore(memstore.StoreConfig{})
	if err != nil {
		log.Fatal(err)
	}
	signer, err := limes.NewSigner(limes.SignerConfig{KeyID: key.KeyID, Key: key.Private})
	if err != nil {
		log.Fatal(err)
	}
	issuer, err := limes.NewIssuer(limes.IssuerConfig{Signer: signer, Store: records, IssuerName: "example-issuer"})
	if err != nil {
		log.Fatal(err)
	}
	issued, err := issuer.Issue(context.Background(), limes.IssueRequest{
		ClientID: testClientID,
		IssuedBy: limes.Principal{Subject: "operator@example.com"},
		Subject:  limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: uuid.New(), Subject: "research-agent"},
		Audience: []string{"my-service"},
		TTL:      15 * time.Minute,
		Caveats:  limes.Caveats{Requests: limes.RequestCeiling{Unlimited: true}, Ops: []limes.Op{limes.OpGet}, ResourcePrefixes: []string{"corpus/public/"}},
	})
	if err != nil {
		log.Fatal(err)
	}
	return issued.Token, key.Public()
}

func ExampleDecode() {
	token, _ := exampleToken()
	// For tooling only: nothing here is verified.
	c, err := inspect.Decode(token)
	fmt.Println(err, c.Issuer, c.Subject.Subject, c.Caveats.Ops, c.Caveats.ResourcePrefixes)

	_, err = inspect.Decode("not-a-token")
	fmt.Println(errors.Is(err, limes.ErrInvalidToken))
	// Output:
	// <nil> example-issuer research-agent [get] [corpus/public/]
	// true
}

func ExampleVerifySignature() {
	token, pub := exampleToken()
	fmt.Println(inspect.VerifySignature(token, pub))

	other, _, _ := ed25519.GenerateKey(rand.Reader)
	fmt.Println(errors.Is(inspect.VerifySignature(token, other), limes.ErrInvalidToken))
	fmt.Println(errors.Is(inspect.VerifySignature(token, ed25519.PublicKey{1, 2}), inspect.ErrInvalidKey))
	// Output:
	// <nil>
	// true
	// true
}
