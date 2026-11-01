package exchangetest_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/exchange"
	"github.com/oleg-tkachuk/limes/exchange/exchangetest"
	"github.com/oleg-tkachuk/limes/exchange/jwtverify"
	"github.com/oleg-tkachuk/limes/memstore"
)

const (
	issuer   = "https://idp.example.com"
	kid      = "idp-key-1"
	client   = "mcp-client"
	audience = "https://mcp.example.com"
	subject  = "user-1"
	lifetime = 5 * time.Minute
	// longLifetime is the life of the one-per-token suite's subject tokens,
	// and shortTTL its policy's: a capability expires, and is purged, while
	// its subject token can still be presented.
	longLifetime = time.Hour
	shortTTL     = time.Minute
	// sessionID is the identity provider's session a token names.
	sessionID = "idp-session-1"
	// jkt is a DPoP key's thumbprint the bound token names.
	jkt = "0ZcOCORZNYy-DWpqq30jZyJGHTN0d2HglBV3uiguA4I"
)

var now = time.Unix(1_800_000_000, 0)

// provider is an identity provider: its key, and its public key set.
type provider struct {
	key  ed25519.PrivateKey
	keys jwk.Set
}

func newProvider(t *testing.T) provider {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := jwk.Import(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	if err := pub.Set(jwk.KeyIDKey, kid); err != nil {
		t.Fatal(err)
	}
	if err := pub.Set(jwk.AlgorithmKey, jwa.EdDSAEd25519()); err != nil {
		t.Fatal(err)
	}
	set := jwk.NewSet()
	if err := set.AddKey(pub); err != nil {
		t.Fatal(err)
	}
	return provider{key: key, keys: set}
}

// sign is an RFC 9068 access token with claims changed by changes, a nil
// value removing a claim.
func (p provider) sign(t *testing.T, changes map[string]any) string {
	t.Helper()
	claims := map[string]any{
		"iss": issuer, "sub": subject, "aud": audience, "client_id": client,
		"iat": now.Unix(), "exp": now.Add(lifetime).Unix(), "jti": uuid.New().String(),
	}
	for k, v := range changes {
		if v == nil {
			delete(claims, k)
		} else {
			claims[k] = v
		}
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	headers := jws.NewHeaders()
	if err := headers.Set(jws.TypeKey, jwtverify.TypeAccessToken); err != nil {
		t.Fatal(err)
	}
	if err := headers.Set(jws.KeyIDKey, kid); err != nil {
		t.Fatal(err)
	}
	token, err := jws.Sign(payload, jws.WithKey(jwa.EdDSAEd25519(), p.key, jws.WithProtectedHeaders(headers)))
	if err != nil {
		t.Fatal(err)
	}
	return string(token)
}

// The module's own JWTVerifier keeps the contract it sets for others.
func TestJWTVerifierKeepsTheContract(t *testing.T) {
	p, other := newProvider(t), newProvider(t)
	v, err := jwtverify.New(jwtverify.Config{
		Issuer: issuer, Keys: jwtverify.StaticKeys(p.keys), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	exchangetest.TestSubjectVerifier(t, v, exchangetest.Tokens{
		Valid: func(t *testing.T) (string, exchange.Subject) {
			return p.sign(t, nil), exchange.Subject{
				Issuer: issuer, Subject: subject, ClientID: client, Audience: []string{audience}, ExpiresAt: now.Add(lifetime),
			}
		},
		Expired:     func(t *testing.T) string { return p.sign(t, map[string]any{"exp": now.Add(-lifetime).Unix()}) },
		OtherIssuer: func(t *testing.T) string { return p.sign(t, map[string]any{"iss": "https://other.example.com"}) },
		Forged:      func(t *testing.T) string { return other.sign(t, nil) },
		NoClient:    func(t *testing.T) string { return p.sign(t, map[string]any{"client_id": nil}) },
		Bound: func(t *testing.T) (string, string) {
			return p.sign(t, map[string]any{"cnf": map[string]any{"jkt": jkt}}), jkt
		},
		Session: func(t *testing.T) (string, string) {
			return p.sign(t, map[string]any{"sid": sessionID}), sessionID
		},
	})
}

// resource is the target the one-per-token exchanges ask for.
const resource = "https://tools.example.com"

// The module's own Exchanger, over memstore, keeps the one-per-token
// contract it sets for others.
func TestOnePerTokenOverMemstore(t *testing.T) {
	p := newProvider(t)
	var (
		mu      sync.Mutex
		elapsed time.Duration
	)
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now.Add(elapsed)
	}
	subjects, err := jwtverify.New(jwtverify.Config{Issuer: issuer, Keys: jwtverify.StaticKeys(p.keys), Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	store, err := memstore.NewStore(memstore.StoreConfig{Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	key, err := limes.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := limes.NewSigner(limes.SignerConfig{KeyID: key.KeyID, Key: key.Private})
	if err != nil {
		t.Fatal(err)
	}
	iss, err := limes.NewIssuer(limes.IssuerConfig{Signer: signer, Store: store, IssuerName: "limes-exchange", Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	tenant := uuid.New()
	ex, err := exchange.NewExchanger(exchange.Config{
		Issuer: iss, Subjects: subjects, Exchanges: store, Now: clock, Logger: slog.New(slog.DiscardHandler),
		Policy: func(_ context.Context, g exchange.Grant) (limes.IssueRequest, error) {
			return limes.IssueRequest{
				IssuedBy: limes.Principal{Type: limes.PrincipalTypeService, Subject: audience},
				Subject:  limes.Principal{Type: limes.PrincipalTypeUser, TenantID: tenant, Subject: g.Subject.Subject},
				Caveats:  limes.Caveats{Requests: limes.RequestCeiling{Max: 1}, Ops: []limes.Op{limes.OpGet}},
				TTL:      shortTTL,
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	exchangeOf := func(token string) exchange.Request {
		return exchange.Request{
			Client:       exchange.Client{ID: audience, SubjectAudiences: []string{audience}},
			SubjectToken: token, Resources: []string{resource},
		}
	}
	exchangetest.TestOnePerToken(t, exchangetest.OnePerToken{
		Exchanger: ex,
		Request: func(t *testing.T) exchange.Request {
			return exchangeOf(p.sign(t, map[string]any{"exp": clock().Add(longLifetime).Unix()}))
		},
		NoJTI: func(t *testing.T) exchange.Request { return exchangeOf(p.sign(t, map[string]any{"jti": nil})) },
		Revoke: func(t *testing.T, id uuid.UUID) {
			if _, err := store.Revoke(t.Context(), limes.RevokeRequest{CapabilityID: id}); err != nil {
				t.Fatal(err)
			}
		},
		Purge: func(t *testing.T) {
			mu.Lock()
			elapsed += shortTTL + limes.MinPurgeRetention + time.Minute
			mu.Unlock()
			if _, err := store.Purge(t.Context(), limes.PurgeRequest{}); err != nil {
				t.Fatal(err)
			}
		},
	})
}
