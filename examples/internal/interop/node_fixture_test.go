package interop

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/jwks"
	"github.com/oleg-tkachuk/limes/memstore"
)

// The fixture testdata/node/fixture.json holds limes tokens, the JWKS they
// verify under and the time to verify them at, for the Node test beside it
// (testdata/node/interop.test.ts), which checks that the jose library reads
// them. LIMES_WRITE_NODE_FIXTURE=1 writes it anew; every run checks that
// the fixture is still a capability limes verifies, so a change of the token
// format fails here until the fixture is written again.
const (
	nodeFixtureFile  = "testdata/node/fixture.json"
	nodeFixtureEnv   = "LIMES_WRITE_NODE_FIXTURE"
	nodeFixtureWrite = "1"
	// nodeVerifyAfter is how long after issuance the fixture is verified.
	nodeVerifyAfter = time.Minute
)

// nodeIssuedAt is when the fixture's tokens were issued.
var nodeIssuedAt = time.Unix(1_800_000_000, 0).UTC()

// nodeFixture is fixture.json.
type nodeFixture struct {
	Issuer   string          `json:"issuer"`
	Audience string          `json:"audience"`
	Subject  string          `json:"subject"`
	Tenant   string          `json:"tenant"`
	Resource string          `json:"resource"`
	VerifyAt int64           `json:"verify_at"`
	JWKS     json.RawMessage `json:"jwks"`
	// Tokens are one per algorithm name the signer writes.
	Tokens map[limes.SigningAlgorithm]string `json:"tokens"`
}

// writeNodeFixture issues a capability under each name of Ed25519, each
// with a key of its own whose JWKS entry names it, and writes the fixture.
func writeNodeFixture(t *testing.T) {
	t.Helper()
	records, err := memstore.NewStore(memstore.StoreConfig{})
	if err != nil {
		t.Fatal(err)
	}
	tenant := uuid.New()
	fx := nodeFixture{
		Issuer: issuerName, Audience: audience, Subject: readerSubject, Tenant: tenant.String(), Resource: resourcePath,
		VerifyAt: nodeIssuedAt.Add(nodeVerifyAfter).Unix(), Tokens: map[limes.SigningAlgorithm]string{},
	}
	keys, names := map[string]ed25519.PublicKey{}, map[string]string{}
	for _, alg := range []limes.SigningAlgorithm{limes.SigningAlgorithmEd25519, limes.SigningAlgorithmEdDSA} {
		key, err := limes.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		keys[key.KeyID], names[key.KeyID] = key.Public(), string(alg)
		signer, err := limes.NewSigner(limes.SignerConfig{KeyID: key.KeyID, Key: key.Private, Algorithm: alg})
		if err != nil {
			t.Fatal(err)
		}
		issuer, err := limes.NewIssuer(limes.IssuerConfig{
			Signer: signer, Store: records, IssuerName: issuerName, Now: func() time.Time { return nodeIssuedAt },
		})
		if err != nil {
			t.Fatal(err)
		}
		res, err := issuer.Issue(context.Background(), limes.IssueRequest{
			IssuedBy: limes.Principal{Type: limes.PrincipalTypeUser, TenantID: tenant, Subject: operatorSubject},
			Subject:  limes.Principal{Type: limes.PrincipalTypeService, TenantID: tenant, Subject: readerSubject},
			Audience: []string{audience}, TTL: tokenTTL,
			Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}, ResourcePrefixes: []string{resourcePath}},
		})
		if err != nil {
			t.Fatal(err)
		}
		fx.Tokens[alg] = res.Token
	}
	if fx.JWKS, err = jwks.MarshalAlgorithms(keys, names); err != nil {
		t.Fatal(err)
	}
	doc, err := json.MarshalIndent(fx, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nodeFixtureFile, append(doc, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

// notRevoked answers that nothing is revoked: the fixture's records are
// gone with the process that wrote it.
type notRevoked struct{}

func (notRevoked) IsRevoked(context.Context, uuid.UUID) (bool, error) { return false, nil }

// The Node test's fixture is a capability under each name of Ed25519, that
// limes verifies as written.
func TestNodeFixtureIsCurrent(t *testing.T) {
	if os.Getenv(nodeFixtureEnv) == nodeFixtureWrite {
		writeNodeFixture(t)
	}
	raw, err := os.ReadFile(filepath.Clean(nodeFixtureFile))
	if err != nil {
		t.Fatal(err)
	}
	var fx nodeFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	set, err := jwks.Parse(fx.JWKS)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := jwks.NewStatic(jwks.StaticConfig{Keys: set})
	if err != nil {
		t.Fatal(err)
	}
	v, err := limes.NewVerifier(limes.VerifierConfig{
		Keys: keys, Revocations: notRevoked{}, TrustedIssuers: []string{fx.Issuer},
		Now: func() time.Time { return time.Unix(fx.VerifyAt, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, alg := range []limes.SigningAlgorithm{limes.SigningAlgorithmEd25519, limes.SigningAlgorithmEdDSA} {
		token, ok := fx.Tokens[alg]
		if !ok {
			t.Errorf("no %s token in %s", alg, nodeFixtureFile)
			continue
		}
		if _, err := v.Verify(t.Context(), limes.VerifyRequest{Token: token, Audience: fx.Audience}); err != nil {
			t.Errorf("the %s token of %s no longer verifies (%v): write it again with %s=%s", alg, nodeFixtureFile, err, nodeFixtureEnv, nodeFixtureWrite)
		}
	}
}
