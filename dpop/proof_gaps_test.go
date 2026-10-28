package dpop

import (
	"context"
	"crypto"
	"encoding/json"
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes/internal/jose"
)

// craftProof signs a proof as Sign would, after edit has changed its header
// and claims and sig its signature, so a check can present what Sign never
// writes.
func craftProof(t *testing.T, key crypto.Signer, at time.Time, edit func(*proofHeader, *proofClaims), sig func([]byte) []byte) string {
	t.Helper()
	jwk, err := publicJWK(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	alg := jose.AlgEd25519
	if jwk.Kty == jose.KtyEC {
		alg = jose.AlgES256
	}
	htu, err := canonicalHTU(dpopURL)
	if err != nil {
		t.Fatal(err)
	}
	h := proofHeader{Typ: tokenType, Alg: alg, JWK: &jwk}
	c := proofClaims{JTI: "jti-" + at.String(), HTM: "POST", HTU: htu, IAT: float64(at.Unix()), ATH: accessTokenHash("the-token")}
	if edit != nil {
		edit(&h, &c)
	}
	header, _ := json.Marshal(h)
	claims, _ := json.Marshal(c)
	return signCrafted(t, key, header, claims, sig)
}

// craftProofWith signs a proof as Sign would, with members added to its
// header: what a client may put there that Sign never writes.
func craftProofWith(t *testing.T, key crypto.Signer, at time.Time, members map[string]any) string {
	t.Helper()
	var header, claims map[string]any
	parts, err := jose.Split(craftProof(t, key, at, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := decodeSegment(parts.Header, &header); err != nil {
		t.Fatal(err)
	}
	if err := decodeSegment(parts.Payload, &claims); err != nil {
		t.Fatal(err)
	}
	maps.Copy(header, members)
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(claims)
	return signCrafted(t, key, h, c, nil)
}

// signCrafted is the compact JWS of header and claims, signed with key, its
// signature passed through sig when there is one.
func signCrafted(t *testing.T, key crypto.Signer, header, claims []byte, sig func([]byte) []byte) string {
	t.Helper()
	input := jose.B64(header) + "." + jose.B64(claims)
	s, err := signProof(context.Background(), key, []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if sig != nil {
		s = sig(s)
	}
	return input + "." + jose.B64(s)
}

// RFC 7515 §4.1.11: a crit header parameter names extensions the recipient
// must understand, and this package understands none, so a proof naming
// any — or carrying a crit that names nothing — is refused. A member it
// does not know and no crit names is ignored (RFC 7515 §4).
func TestDPoPRefusesAProofWithCriticalExtensions(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	key := dpopKeys(t)["Ed25519"]
	const extension = "x"
	for name, tc := range map[string]struct {
		members map[string]any
		want    error
	}{
		"an extension no crit names": {map[string]any{extension: 1}, nil},
		"crit naming an extension":   {map[string]any{headerCrit: []string{extension}, extension: 1}, ErrInvalid},
		"an empty crit":              {map[string]any{headerCrit: []string{}}, ErrInvalid},
		"a null crit":                {map[string]any{headerCrit: nil}, ErrInvalid},
	} {
		err := verifyAt(t, VerifierConfig{}, key, now, craftProofWith(t, key, now, tc.members))
		if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
}

// padS is an ES256 signature with a zero byte before s: the same r and s,
// one byte too long.
func padS(sig []byte) []byte {
	padded := append([]byte{}, sig[:jose.P256CoordinateBytes]...)
	padded = append(padded, 0)
	return append(padded, sig[jose.P256CoordinateBytes:]...)
}

// verifyAt verifies proof as one for the-token, bound to key, at now.
func verifyAt(t *testing.T, cfg VerifierConfig, key crypto.Signer, now time.Time, proof string) error {
	t.Helper()
	cfg.Replay = newReplay(t, MemoryReplayCacheConfig{})
	cfg.Now = func() time.Time { return now }
	v := newVerifier(t, cfg)
	return v.Verify(context.Background(), Request{
		Proof: Proof{JWT: proof, Method: "POST", URL: dpopURL}, Token: "the-token", JKT: boundJKT(t, key), Holder: testHolder,
	})
}

// A proof is refused for what RFC 9449 §4.3 requires of its header and
// signature, each on its own: the type, an algorithm that is the key's, and
// a signature of exactly its length.
func TestDPoPRefusesAProofTheRFCDoesNotAllow(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	keys := dpopKeys(t)
	ed, ec := keys["Ed25519"], keys["P-256"]
	for name, tc := range map[string]struct {
		key  crypto.Signer
		edit func(*proofHeader, *proofClaims)
		sig  func([]byte) []byte
		want error
	}{
		"as Sign writes it":       {ed, nil, nil, nil},
		"another type":            {ed, func(h *proofHeader, _ *proofClaims) { h.Typ = "JWT" }, nil, ErrInvalid},
		"an EC alg on an Ed25519": {ed, func(h *proofHeader, _ *proofClaims) { h.Alg = jose.AlgES256 }, nil, ErrInvalid},
		"an Ed25519 alg on an EC": {ec, func(h *proofHeader, _ *proofClaims) { h.Alg = jose.AlgEd25519 }, nil, ErrInvalid},
		// A zero byte leading s leaves its value, and so the signature,
		// valid: only the length check refuses it.
		"an ES256 signature padded": {ec, nil, padS, ErrInvalid},
	} {
		err := verifyAt(t, VerifierConfig{}, tc.key, now, craftProof(t, tc.key, now, tc.edit, tc.sig))
		if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
}

// MaxProofBytes is a bound of its own: a proof one byte past it is refused
// before it is read, whatever else is in it.
func TestDPoPBoundsTheProofItReads(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	key := dpopKeys(t)["Ed25519"]
	proof := craftProof(t, key, now, nil, nil)
	if err := verifyAt(t, VerifierConfig{MaxProofBytes: len(proof)}, key, now, proof); err != nil {
		t.Errorf("a proof at MaxProofBytes: %v", err)
	}
	if err := verifyAt(t, VerifierConfig{MaxProofBytes: len(proof) - 1}, key, now, proof); !errors.Is(err, ErrInvalid) {
		t.Errorf("a proof a byte past MaxProofBytes: err = %v, want ErrInvalid", err)
	}
}

// The iat window is exact on both sides, and a fractional iat counts its
// fraction: half a second past the window is past it.
func TestDPoPWindowEdges(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	key := dpopKeys(t)["Ed25519"]
	const half = 0.5
	edge := DefaultWindow.Seconds()
	for name, tc := range map[string]struct {
		iat  float64
		want error
	}{
		"at the past edge":                             {float64(now.Unix()) - edge, nil},
		"a second past the past edge":                  {float64(now.Unix()) - edge - 1, ErrInvalid},
		"at the future edge":                           {float64(now.Unix()) + edge, nil},
		"a second past the future":                     {float64(now.Unix()) + edge + 1, ErrInvalid},
		"half a second past the future, as a fraction": {float64(now.Unix()) + edge + half, ErrInvalid},
	} {
		proof := craftProof(t, key, now, func(_ *proofHeader, c *proofClaims) { c.IAT = tc.iat }, nil)
		err := verifyAt(t, VerifierConfig{}, key, now, proof)
		if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
}

// Past MaxEntries the cache refuses every new id, whichever holder it is
// for: the per-holder share does not lift the bound on the whole.
func TestMemoryReplayCacheBoundsTheWhole(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	const entries = 2
	c := newReplay(t, MemoryReplayCacheConfig{MaxEntries: entries, EntriesPerHolder: 1})
	c.now = func() time.Time { return now }
	for _, holder := range []string{"first", "second"} {
		if seenAs(t, c, holder, "key-"+holder, "jti", now.Add(time.Minute)) {
			t.Fatalf("holder %s refused within the bound", holder)
		}
	}
	if !seenAs(t, c, "third", "key-third", "jti", now.Add(time.Minute)) {
		t.Error("a third holder recorded past MaxEntries")
	}
}
