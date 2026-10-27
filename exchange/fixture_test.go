package exchange

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"maps"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/dpop"
	"github.com/oleg-tkachuk/limes/jwks"
	"github.com/oleg-tkachuk/limes/memstore"
)

const (
	// The identity provider, its signing key's kid, and the client its
	// tokens are issued to.
	idpIssuer = "https://idp.example.com"
	idpKID    = "idp-key-1"
	idpClient = "mcp-client"
	// mcpServer is the exchanging MCP server: the audience of the tokens it
	// receives, and the client of the exchange.
	mcpServer = "https://mcp.example.com"
	// toolResource is the resource the capability is asked for.
	toolResource = "https://tools.example.com"
	// The capability's issuer, and the user the tokens name.
	limesIssuer = "limes-exchange"
	userSubject = "user-1"
	// subjectTTL is the subject token's lifetime.
	subjectTTL = 5 * time.Minute
	// tokenEndpoint is the exchange's URL, which DPoP proofs name.
	tokenEndpoint = "https://exchange.example.com/token"
)

// testNow is the fixed clock of every test.
var testNow = time.Unix(1_800_000_000, 0)

func clock() time.Time { return testNow }

// idp is an identity provider: a key and the key set it publishes.
type idp struct {
	key  ed25519.PrivateKey
	keys jwk.Set
}

func newIDP(t testing.TB) *idp {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := jwk.Import(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{jwk.KeyIDKey: idpKID, jwk.AlgorithmKey: jwa.EdDSAEd25519()} {
		if err := pub.Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	set := jwk.NewSet()
	if err := set.AddKey(pub); err != nil {
		t.Fatal(err)
	}
	return &idp{key: key, keys: set}
}

// accessClaims are an RFC 9068 access token's claims for the MCP server,
// issued at testNow.
func accessClaims() map[string]any {
	return map[string]any{
		claimIssuer: idpIssuer, claimSubject: userSubject, claimAudience: mcpServer, ClaimClientID: idpClient,
		claimIssuedAt: testNow.Unix(), claimExpiry: testNow.Add(subjectTTL).Unix(), claimJWTID: uuid.New().String(),
		claimScope: "tools:read tools:write",
	}
}

// sign is claims signed by the identity provider under typ.
func (p *idp) sign(t testing.TB, typ string, claims map[string]any) string {
	t.Helper()
	return signWith(t, p.key, jwa.EdDSAEd25519(), typ, claims)
}

// signWith is claims signed by key under alg and typ, with the provider's kid.
func signWith(t testing.TB, key crypto.Signer, alg jwa.SignatureAlgorithm, typ string, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	headers := jws.NewHeaders()
	for name, value := range map[string]any{jws.TypeKey: typ, jws.KeyIDKey: idpKID} {
		if err := headers.Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	token, err := jws.Sign(payload, jws.WithKey(alg, key, jws.WithProtectedHeaders(headers)))
	if err != nil {
		t.Fatal(err)
	}
	return string(token)
}

// with is claims with changes applied: a nil value removes the claim.
func with(claims map[string]any, changes map[string]any) map[string]any {
	out := maps.Clone(claims)
	for k, v := range changes {
		if v == nil {
			delete(out, k)
		} else {
			out[k] = v
		}
	}
	return out
}

// countingStore is a memstore that counts the capabilities it records.
type countingStore struct {
	*memstore.Store
	inserts atomic.Int64
}

func (s *countingStore) Insert(ctx context.Context, req limes.InsertRequest) error {
	s.inserts.Add(1)
	return s.Store.Insert(ctx, req)
}

// fixture is an identity provider, an exchanger over a limes issuer, and a
// verifier of what it issues.
type fixture struct {
	idp      *idp
	tenant   uuid.UUID
	store    *countingStore
	ex       *Exchanger
	verifier *limes.Verifier
	nonces   *dpop.HMACNonces
}

type fixtureConfig struct {
	policy Policy
	nonces bool
	noDPoP bool
	// narrow lets the exchanger narrow capabilities.
	narrow bool
}

func newFixture(t testing.TB, fc fixtureConfig) *fixture {
	t.Helper()
	f := &fixture{idp: newIDP(t), tenant: uuid.New()}
	if fc.policy == nil {
		fc.policy = f.readPolicy
	}
	mem, err := memstore.NewStore(memstore.StoreConfig{})
	if err != nil {
		t.Fatal(err)
	}
	f.store = &countingStore{Store: mem}
	key, err := limes.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := limes.NewSigner(limes.SignerConfig{KeyID: key.KeyID, Key: key.Private})
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := limes.NewIssuer(limes.IssuerConfig{Signer: signer, Store: f.store, IssuerName: limesIssuer, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	subjects, err := NewJWTVerifier(JWTVerifierConfig{Issuer: idpIssuer, Keys: StaticKeys(f.idp.keys), Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Issuer: issuer, Subjects: subjects, Policy: fc.policy, Now: clock, Logger: slog.New(slog.DiscardHandler)}
	if !fc.noDPoP {
		replay, err := dpop.NewMemoryReplayCache(dpop.MemoryReplayCacheConfig{})
		if err != nil {
			t.Fatal(err)
		}
		dcfg := dpop.VerifierConfig{Replay: replay, Now: clock}
		if fc.nonces {
			f.nonces, err = dpop.NewHMACNonces(dpop.HMACNoncesConfig{Keys: dpop.SealNonceKeys(make([]byte, dpop.HMACNonceKeyBytes)), Now: clock})
			if err != nil {
				t.Fatal(err)
			}
			dcfg.Nonces = f.nonces
		}
		if cfg.DPoP, err = dpop.NewVerifier(dcfg); err != nil {
			t.Fatal(err)
		}
	}
	keys, err := jwks.NewStatic(jwks.StaticConfig{Keys: map[string]ed25519.PublicKey{key.KeyID: key.Public()}})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := dpop.NewMemoryReplayCache(dpop.MemoryReplayCacheConfig{})
	if err != nil {
		t.Fatal(err)
	}
	proofs, err := dpop.NewVerifier(dpop.VerifierConfig{Replay: replay, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	if f.verifier, err = limes.NewVerifier(limes.VerifierConfig{
		Keys: keys, Revocations: mem, TrustedIssuers: []string{limesIssuer}, Now: clock, DPoP: proofs,
	}); err != nil {
		t.Fatal(err)
	}
	if fc.narrow {
		cfg.Capabilities, cfg.CapabilityAudience, cfg.NarrowPolicy = f.verifier, exchangeAudience, f.narrowPolicy
	}
	if f.ex, err = NewExchanger(cfg); err != nil {
		t.Fatal(err)
	}
	return f
}

// readPolicy grants the token's user a capability to read, for the
// resources asked for.
func (f *fixture) readPolicy(_ context.Context, g Grant) (limes.IssueRequest, error) {
	return limes.IssueRequest{
		IssuedBy: limes.Principal{Type: limes.PrincipalTypeService, Subject: mcpServer},
		Subject:  limes.Principal{Type: limes.PrincipalTypeUser, TenantID: f.tenant, Subject: g.Subject.Subject},
		Caveats:  limes.Caveats{Ops: []limes.Op{limes.OpGet}},
	}, nil
}

// client is the MCP server as a client of the exchange.
func client() Client { return Client{ID: mcpServer, SubjectAudiences: []string{mcpServer}} }

// request is an exchange of token for a capability to toolResource.
func request(token string) Request {
	return Request{Client: client(), SubjectToken: token, Resources: []string{toolResource}}
}

// proof is a token request's DPoP proof by key, at testNow.
func proof(t testing.TB, key crypto.Signer, nonce string) dpop.Proof {
	t.Helper()
	jwt, err := dpop.Sign(context.Background(), key, dpop.SignRequest{Method: "POST", URL: tokenEndpoint, Nonce: nonce, IssuedAt: testNow})
	if err != nil {
		t.Fatal(err)
	}
	return dpop.Proof{JWT: jwt, Method: "POST", URL: tokenEndpoint}
}

// clientKey is a fresh key a client holds.
func clientKey(t testing.TB) ed25519.PrivateKey {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// exchangeAudience is the exchange's own audience, which a capability it
// narrows must name, and subAgent the principal a narrowed capability is for.
const (
	exchangeAudience = "https://exchange.example.com"
	subAgent         = "sub-agent"
)

// narrowPolicy delegates the parent's caveats to a sub-agent.
func (f *fixture) narrowPolicy(context.Context, Narrowing) (limes.DelegateRequest, error) {
	return limes.DelegateRequest{Subject: limes.Principal{Type: limes.PrincipalTypeAgent, Subject: subAgent}, InheritCaveats: true}, nil
}
