// Package interop checks limes tokens against JOSE libraries that are not
// limes: that one verifies a capability through the issuer's JWKS and reads
// its claims as RFC 7519 defines them, and that limes accepts the DPoP proofs
// one signs. It lives in the examples module so that the libraries stay out of
// limes's own module graph.
package interop

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	gojwt "github.com/golang-jwt/jwt/v5"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/dpop"
	"github.com/oleg-tkachuk/limes/jwks"
	"github.com/oleg-tkachuk/limes/memstore"
)

const (
	issuerName = "interop-issuer"
	audience   = "interop-api"
	// otherAudience is a service the capabilities are not for.
	otherAudience = "another-api"
	// The principals: who asks for a capability, and who holds it.
	operatorSubject = "operator"
	readerSubject   = "reader"
	tokenTTL        = 15 * time.Minute

	// The capability's own claims, as limes names them on the wire.
	claimPrincipal    = "limes_principal"
	claimCaveats      = "limes_caveats"
	claimConfirmation = "cnf"

	// algEd25519 is the fully-specified algorithm of RFC 9864, which limes
	// signs with.
	algEd25519 = "Ed25519"

	// The DPoP proof's type and claims (RFC 9449 §4.2).
	dpopType     = "dpop+jwt"
	headerJWK    = "jwk"
	requestURL   = "https://api.example.com/resource"
	otherURL     = "https://api.example.com/other"
	httpMethod   = http.MethodGet
	resourcePath = "corpus/public/report.pdf"
)

// fixture is an issuer of capabilities, a verifier of them, and the issuer's
// JWKS served over HTTP, as a resource server outside limes would fetch it.
type fixture struct {
	issuer   *limes.Issuer
	verifier *limes.Verifier
	pub      ed25519.PublicKey
	// key is the issuer's signing key, for tokens another library signs
	// with it.
	key     crypto.Signer
	kid     string
	jwksURL string
	tenant  uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureNaming(t, "")
}

// newFixtureNaming is a fixture whose tokens name alg, the default when empty.
func newFixtureNaming(t *testing.T, alg limes.SigningAlgorithm) *fixture {
	t.Helper()
	key, err := limes.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	records, err := memstore.NewStore(memstore.StoreConfig{})
	if err != nil {
		t.Fatal(err)
	}
	signer, err := limes.NewSigner(limes.SignerConfig{KeyID: key.KeyID, Key: key.Private, Algorithm: alg})
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := limes.NewIssuer(limes.IssuerConfig{Signer: signer, Store: records, IssuerName: issuerName})
	if err != nil {
		t.Fatal(err)
	}
	keySet := map[string]ed25519.PublicKey{key.KeyID: key.Public()}
	keys, err := jwks.NewStatic(jwks.StaticConfig{Keys: keySet})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := dpop.NewMemoryReplayCache(dpop.MemoryReplayCacheConfig{})
	if err != nil {
		t.Fatal(err)
	}
	proofs, err := dpop.NewVerifier(dpop.VerifierConfig{Replay: replay})
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := limes.NewVerifier(limes.VerifierConfig{
		Keys: keys, Revocations: records, TrustedIssuers: []string{issuerName}, DPoP: proofs,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The key's entry names the alg its tokens name, as an issuer signing
	// with EdDSA publishes it.
	names := map[string]string{}
	if alg != "" {
		names[key.KeyID] = string(alg)
	}
	handler, err := jwks.NewHandler(jwks.HandlerConfig{
		Keys:       func() map[string]ed25519.PublicKey { return keySet },
		Algorithms: func() map[string]string { return names },
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &fixture{
		issuer: issuer, verifier: verifier, pub: key.Public(), key: key.Private, kid: key.KeyID,
		jwksURL: srv.URL, tenant: uuid.New(),
	}
}

// issue is a capability to read resourcePath, bound to the key with
// thumbprint jkt when it is not empty.
func (f *fixture) issue(t *testing.T, jkt string) limes.IssueResult {
	t.Helper()
	res, err := f.issuer.Issue(t.Context(), limes.IssueRequest{
		IssuedBy: limes.Principal{Type: limes.PrincipalTypeUser, TenantID: f.tenant, Subject: operatorSubject},
		Subject:  limes.Principal{Type: limes.PrincipalTypeService, TenantID: f.tenant, Subject: readerSubject},
		Audience: []string{audience},
		TTL:      tokenTTL,
		Caveats:  limes.Caveats{Ops: []limes.Op{limes.OpGet}, ResourcePrefixes: []string{resourcePath}},

		ConfirmationJKT: jkt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// fetchKeys is the issuer's JWKS as jwx reads it from the endpoint.
func (f *fixture) fetchKeys(t *testing.T) jwk.Set {
	t.Helper()
	set, err := jwk.Fetch(t.Context(), f.jwksURL)
	if err != nil {
		t.Fatalf("jwx fetching the JWKS: %v", err)
	}
	return set
}

// A resource server using jwx, which knows the algorithm of RFC 9864, verifies
// a capability with the issuer's JWKS and the standard claims alone, and
// refuses what RFC 7519 says to refuse.
func TestJWXVerifiesACapability(t *testing.T) {
	f := newFixture(t)
	token := f.issue(t, "").Token
	set := f.fetchKeys(t)
	parse := func(token string, set jwk.Set, aud string, opts ...jwt.ParseOption) (jwt.Token, error) {
		return jwt.ParseString(token, append([]jwt.ParseOption{
			jwt.WithKeySet(set), jwt.WithIssuer(issuerName), jwt.WithAudience(aud),
		}, opts...)...)
	}

	tok, err := parse(token, set, audience)
	if err != nil {
		t.Fatalf("jwx refused a capability: %v", err)
	}
	if sub, _ := tok.Subject(); sub != readerSubject {
		t.Errorf("sub = %q, want the principal's subject", sub)
	}

	expired := jwt.ClockFunc(func() time.Time { return time.Now().Add(2 * tokenTTL) })
	tampered := tamper(t, token)
	otherSet := keySetOf(t, otherEd25519Key(t), f.kid)
	for name, refused := range map[string]func() error{
		"another audience":          func() error { _, err := parse(token, set, otherAudience); return err },
		"expired":                   func() error { _, err := parse(token, set, audience, jwt.WithClock(expired)); return err },
		"a tampered payload":        func() error { _, err := parse(tampered, set, audience); return err },
		"another key under the kid": func() error { _, err := parse(token, otherSet, audience); return err },
	} {
		if refused() == nil {
			t.Errorf("jwx accepted the capability with %s", name)
		}
	}
}

// A reader outside limes sees the token's type and algorithm in its header,
// and the capability's own claims as JSON beside the standard ones.
func TestJWXReadsTheCapabilityClaims(t *testing.T) {
	f := newFixture(t)
	holder := otherEd25519Key(t)
	jkt, err := dpop.Thumbprint(holder.Public())
	if err != nil {
		t.Fatal(err)
	}
	res := f.issue(t, jkt)

	msg, err := jws.ParseString(res.Token)
	if err != nil {
		t.Fatal(err)
	}
	headers := msg.Signatures()[0].ProtectedHeaders()
	if typ, _ := headers.Type(); typ != string(limes.TokenTypeV6) {
		t.Errorf("typ = %q, want %q", typ, limes.TokenTypeV6)
	}
	if alg, _ := headers.Algorithm(); alg.String() != algEd25519 {
		t.Errorf("alg = %q, want %q", alg, algEd25519)
	}

	tok, err := jwt.ParseString(res.Token, jwt.WithKeySet(f.fetchKeys(t)), jwt.WithIssuer(issuerName), jwt.WithAudience(audience))
	if err != nil {
		t.Fatal(err)
	}
	if jti, _ := tok.JwtID(); jti != res.Capability.ID.String() {
		t.Errorf("jti = %q, want the capability id %s", jti, res.Capability.ID)
	}
	var principal struct {
		TenantID string `json:"tenant_id"`
		Subject  string `json:"subject"`
	}
	var caveats struct {
		Ops              []string `json:"ops"`
		ResourcePrefixes []string `json:"resource_prefixes"`
	}
	var cnf struct {
		JKT string `json:"jkt"`
	}
	for claim, dst := range map[string]any{claimPrincipal: &principal, claimCaveats: &caveats, claimConfirmation: &cnf} {
		if err := getClaim(tok, claim, dst); err != nil {
			t.Fatalf("claim %s: %v", claim, err)
		}
	}
	if principal.TenantID != f.tenant.String() || principal.Subject != readerSubject {
		t.Errorf("%s = %+v", claimPrincipal, principal)
	}
	if !slices.Equal(caveats.Ops, []string{string(limes.OpGet)}) || !slices.Equal(caveats.ResourcePrefixes, []string{resourcePath}) {
		t.Errorf("%s = %+v", claimCaveats, caveats)
	}
	if cnf.JKT != jkt {
		t.Errorf("cnf.jkt = %q, want %q", cnf.JKT, jkt)
	}
}

// getClaim reads a private claim into dst through its JSON, as a reader that
// knows nothing of limes's types would.
func getClaim(tok jwt.Token, name string, dst any) error {
	var v any
	if err := tok.Get(name, &v); err != nil {
		return err
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

// The JWKS describes the key as RFC 8037 and RFC 9864 do, an OKP key on
// Ed25519 for the Ed25519 algorithm, and its kid is the RFC 7638 thumbprint
// jwx computes.
func TestJWXReadsTheJWKS(t *testing.T) {
	f := newFixture(t)
	key, ok := f.fetchKeys(t).LookupKeyID(f.kid)
	if !ok {
		t.Fatalf("the JWKS has no key %s", f.kid)
	}
	if kty := key.KeyType(); kty != jwa.OKP() {
		t.Errorf("kty = %s, want %s", kty, jwa.OKP())
	}
	if okp, ok := key.(jwk.OKPPublicKey); !ok {
		t.Errorf("the key is a %T, not an OKP public key", key)
	} else if crv, _ := okp.Crv(); crv != jwa.Ed25519() {
		t.Errorf("crv = %s, want %s", crv, jwa.Ed25519())
	}
	if alg, _ := key.Algorithm(); alg.String() != algEd25519 {
		t.Errorf("alg = %v, want %s", alg, algEd25519)
	}
	sum, err := key.Thumbprint(crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if got := base64.RawURLEncoding.EncodeToString(sum); got != f.kid || got != jwks.KeyID(f.pub) {
		t.Errorf("jwx thumbprint %s, kid %s", got, f.kid)
	}
}

// limes accepts a DPoP proof jwx signed, under each algorithm it reads, for
// a capability bound to the thumbprint jwx computes; and refuses one for
// another request.
func TestLimesAcceptsJWXProofs(t *testing.T) {
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		alg jwa.SignatureAlgorithm
		key crypto.Signer
	}{
		{jwa.EdDSAEd25519(), otherEd25519Key(t)}, // name tentative in jwx v3.4.0
		{jwa.EdDSA(), otherEd25519Key(t)},
		{jwa.ES256(), ecKey},
	} {
		t.Run(tc.alg.String(), func(t *testing.T) {
			f := newFixture(t)
			pub, err := jwk.Import(tc.key.Public())
			if err != nil {
				t.Fatal(err)
			}
			sum, err := pub.Thumbprint(crypto.SHA256)
			if err != nil {
				t.Fatal(err)
			}
			token := f.issue(t, base64.RawURLEncoding.EncodeToString(sum)).Token

			for _, c := range []struct {
				name     string
				htu      string
				accepted bool
			}{{"the request", requestURL, true}, {"another request", otherURL, false}} {
				proof := signProof(t, tc.alg, tc.key, pub, token, c.htu)
				_, err := f.verifier.Verify(t.Context(), limes.VerifyRequest{
					Token: token, Audience: audience, Scheme: limes.AuthSchemeDPoP,
					DPoP: dpop.Proof{JWT: proof, Method: httpMethod, URL: requestURL},
				})
				if (err == nil) != c.accepted {
					t.Errorf("a proof for %s: %v", c.name, err)
				}
			}
		})
	}
}

// signProof is a DPoP proof for a GET of htu presenting token, signed by jwx.
func signProof(t *testing.T, alg jwa.SignatureAlgorithm, key crypto.Signer, pub jwk.Key, token, htu string) string {
	t.Helper()
	ath := sha256.Sum256([]byte(token))
	payload, err := json.Marshal(map[string]any{
		"jti": uuid.New().String(), "htm": httpMethod, "htu": htu, "iat": time.Now().Unix(),
		"ath": base64.RawURLEncoding.EncodeToString(ath[:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	headers := jws.NewHeaders()
	for name, value := range map[string]any{jws.TypeKey: dpopType, headerJWK: pub} {
		if err := headers.Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	proof, err := jws.Sign(payload, jws.WithKey(alg, key, jws.WithProtectedHeaders(headers)))
	if err != nil {
		t.Fatalf("jwx signing a proof: %v", err)
	}
	return string(proof)
}

// ed25519Alias is golang-jwt's Ed25519 method under the name RFC 9864 gives
// it: golang-jwt v5 knows the algorithm only as EdDSA.
type ed25519Alias struct{ *gojwt.SigningMethodEd25519 }

func (ed25519Alias) Alg() string { return algEd25519 }

// A resource server using golang-jwt verifies a capability once it registers
// the RFC 9864 name for the method it already has.
func TestGolangJWTVerifiesWithAnAlias(t *testing.T) {
	gojwt.RegisterSigningMethod(algEd25519, func() gojwt.SigningMethod { return ed25519Alias{gojwt.SigningMethodEdDSA} })
	f := newFixture(t)
	token := f.issue(t, "").Token
	keyFor := func(pub ed25519.PublicKey) gojwt.Keyfunc {
		return func(tok *gojwt.Token) (any, error) {
			if tok.Header["kid"] != f.kid {
				return nil, gojwt.ErrTokenUnverifiable
			}
			return pub, nil
		}
	}
	parse := func(pub ed25519.PublicKey, aud string) error {
		_, err := gojwt.Parse(token, keyFor(pub),
			gojwt.WithValidMethods([]string{algEd25519}), gojwt.WithIssuer(issuerName), gojwt.WithAudience(aud))
		return err
	}
	if err := parse(f.pub, audience); err != nil {
		t.Fatalf("golang-jwt refused a capability: %v", err)
	}
	if parse(otherEd25519Key(t).Public().(ed25519.PublicKey), audience) == nil {
		t.Error("golang-jwt accepted the capability under another key")
	}
	if parse(f.pub, otherAudience) == nil {
		t.Error("golang-jwt accepted the capability for another audience")
	}
}

func otherEd25519Key(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

// keySetOf is a JWKS of priv's public key under kid, with the algorithm limes
// writes.
func keySetOf(t *testing.T, priv ed25519.PrivateKey, kid string) jwk.Set {
	t.Helper()
	raw, err := jwks.Marshal(map[string]ed25519.PublicKey{kid: priv.Public().(ed25519.PublicKey)})
	if err != nil {
		t.Fatal(err)
	}
	set, err := jwk.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// tamper is token with another subject in its claims and its signature
// kept: valid in every claim a reader checks, so only the signature is wrong.
func tamper(t *testing.T, token string) string {
	t.Helper()
	subject, intruder := `"`+readerSubject+`"`, `"intruder"`
	parts := strings.Split(token, ".")
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.ReplaceAll(string(payload), subject, intruder)
	if changed == string(payload) {
		t.Fatalf("no %s in %s", subject, payload)
	}
	parts[1] = base64.RawURLEncoding.EncodeToString([]byte(changed))
	return strings.Join(parts, ".")
}
