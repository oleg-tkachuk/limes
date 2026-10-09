package limes

import (
	"container/heap"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Proof of possession, after RFC 9449 (DPoP).
//
// A capability may be bound to a key its holder keeps: the token then carries
// the key's RFC 7638 thumbprint in `cnf.jkt`, and every request that presents
// it must also carry a DPoP proof — a short JWT signed with that key over the
// request's method and URL, the hash of the token, a unique id and the time.
// A token copied out of a prompt or a log is useless without the key.

// DPoPHeader is the HTTP header a proof travels in.
const DPoPHeader = "DPoP"

// DPoPTokenType is the JWT "typ" of a proof.
const DPoPTokenType = "dpop+jwt"

// DefaultDPoPWindow is how far a proof's iat may be from the verifier's clock.
const DefaultDPoPWindow = time.Minute

// JWS algorithms and JWK members a proof may use (RFC 7518, RFC 8037).
const (
	algEdDSA      = "EdDSA"
	algES256      = "ES256"
	jwkKtyOKP     = "OKP"
	jwkKtyEC      = "EC"
	jwkCrvEd25519 = "Ed25519"
	jwkCrvP256    = "P-256"
)

const (
	// p256CoordinateBytes is the size of a P-256 point coordinate, and of
	// each half of an ES256 signature.
	p256CoordinateBytes = 32
	// es256SignatureBytes is a JWS ES256 signature: r || s, not DER.
	es256SignatureBytes = 2 * p256CoordinateBytes
	// dpopJTIBytes is the randomness in a proof's jti.
	dpopJTIBytes = 16
	// defaultReplayCacheEntries bounds a MemoryReplayCache built with no
	// explicit bound.
	defaultReplayCacheEntries = 100_000
)

// Proof-of-possession rejections. Each also matches ErrInvalidSignature, so a
// consumer that maps only that sentinel still refuses them.
var (
	// ErrDPoPRequired — the capability is key-bound and no proof came with it.
	ErrDPoPRequired error = &popError{"limes: DPoP proof required"}
	// ErrDPoPInvalid — a proof came, and it does not prove possession for
	// this token, method, URL and moment.
	ErrDPoPInvalid error = &popError{"limes: invalid DPoP proof"}
	// ErrDPoPReplayed — a proof's jti was already used inside the window.
	ErrDPoPReplayed error = &popError{"limes: DPoP proof replayed"}
)

type popError struct{ msg string }

func (e *popError) Error() string        { return e.msg }
func (e *popError) Is(target error) bool { return target == ErrInvalidSignature }

type dpopHeader struct {
	Typ string   `json:"typ"`
	Alg string   `json:"alg"`
	JWK *DPoPJWK `json:"jwk"`
}

type dpopClaims struct {
	JTI string `json:"jti"`
	HTM string `json:"htm"`
	HTU string `json:"htu"`
	IAT int64  `json:"iat"`
	ATH string `json:"ath,omitempty"`
}

// DPoPJWK is the public key a proof carries in its header: an Ed25519 (OKP)
// or P-256 (EC) key.
type DPoPJWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y,omitempty"`
}

// PublicJWK describes pub, an ed25519.PublicKey or a P-256 *ecdsa.PublicKey,
// as a JWK.
func PublicJWK(pub crypto.PublicKey) (DPoPJWK, error) {
	switch k := pub.(type) {
	case ed25519.PublicKey:
		return DPoPJWK{Kty: jwkKtyOKP, Crv: jwkCrvEd25519, X: b64(k)}, nil
	case *ecdsa.PublicKey:
		if k.Curve != elliptic.P256() {
			return DPoPJWK{}, errors.New("limes: DPoP EC keys must be P-256")
		}
		raw, err := k.Bytes() // 0x04 || X || Y
		if err != nil {
			return DPoPJWK{}, fmt.Errorf("limes: DPoP EC key: %w", err)
		}
		x, y := raw[1:1+p256CoordinateBytes], raw[1+p256CoordinateBytes:]
		return DPoPJWK{Kty: jwkKtyEC, Crv: jwkCrvP256, X: b64(x), Y: b64(y)}, nil
	}
	return DPoPJWK{}, fmt.Errorf("limes: unsupported DPoP key type %T", pub)
}

// Thumbprint is the key's RFC 7638 SHA-256 thumbprint, base64url — the value
// a key-bound capability carries as its ConfirmationJKT.
func (j DPoPJWK) Thumbprint() (string, error) {
	// RFC 7638: the required members only, lexicographic order, no spaces.
	var canonical string
	switch j.Kty {
	case jwkKtyOKP:
		canonical = fmt.Sprintf(`{"crv":%q,"kty":%q,"x":%q}`, j.Crv, jwkKtyOKP, j.X)
	case jwkKtyEC:
		canonical = fmt.Sprintf(`{"crv":%q,"kty":%q,"x":%q,"y":%q}`, j.Crv, jwkKtyEC, j.X, j.Y)
	default:
		return "", fmt.Errorf("limes: unsupported JWK kty %q", j.Kty)
	}
	sum := sha256.Sum256([]byte(canonical))
	return b64(sum[:]), nil
}

func (j DPoPJWK) publicKey() (crypto.PublicKey, error) {
	x, err := base64.RawURLEncoding.DecodeString(j.X)
	if err != nil {
		return nil, err
	}
	switch {
	case j.Kty == jwkKtyOKP && j.Crv == jwkCrvEd25519:
		if len(x) != ed25519.PublicKeySize {
			return nil, errors.New("Ed25519 key wrong length")
		}
		return ed25519.PublicKey(x), nil
	case j.Kty == jwkKtyEC && j.Crv == jwkCrvP256:
		y, err := base64.RawURLEncoding.DecodeString(j.Y)
		if err != nil || len(x) != p256CoordinateBytes || len(y) != p256CoordinateBytes {
			return nil, errors.New("P-256 key malformed")
		}
		raw := append(append([]byte{4}, x...), y...)
		return ecdsa.ParseUncompressedPublicKey(elliptic.P256(), raw)
	}
	return nil, fmt.Errorf("unsupported JWK %s/%s", j.Kty, j.Crv)
}

// KeyThumbprint is PublicJWK followed by Thumbprint: what to put in an
// IssueRequest's ConfirmationJKT to bind a capability to the holder of pub.
func KeyThumbprint(pub crypto.PublicKey) (string, error) {
	j, err := PublicJWK(pub)
	if err != nil {
		return "", err
	}
	return j.Thumbprint()
}

// AccessTokenHash is the proof's `ath`: base64url(SHA-256(token)).
func AccessTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return b64(sum[:])
}

// NewDPoPProof signs a proof for one request. key is any crypto.Signer whose
// public key is Ed25519 or ECDSA P-256; method and rawURL are the request's; token is
// the capability the request presents.
func NewDPoPProof(key crypto.Signer, method, rawURL, token string, now time.Time) (string, error) {
	jwk, err := PublicJWK(key.Public())
	if err != nil {
		return "", err
	}
	alg := algEdDSA
	if jwk.Kty == jwkKtyEC {
		alg = algES256
	}
	jti := make([]byte, dpopJTIBytes)
	if _, err := rand.Read(jti); err != nil {
		return "", fmt.Errorf("limes: DPoP jti: %w", err)
	}
	htu, err := canonicalHTU(rawURL)
	if err != nil {
		return "", err
	}
	header, _ := json.Marshal(dpopHeader{Typ: DPoPTokenType, Alg: alg, JWK: &jwk})
	claims, _ := json.Marshal(dpopClaims{
		JTI: b64(jti), HTM: strings.ToUpper(method), HTU: htu, IAT: now.Unix(), ATH: AccessTokenHash(token),
	})
	input := b64(header) + "." + b64(claims)
	sig, err := signDPoP(key, []byte(input))
	if err != nil {
		return "", err
	}
	return input + "." + b64(sig), nil
}

// signDPoP signs through the crypto.Signer interface alone, so a key held
// in a KMS or an HSM works as well as one in memory.
func signDPoP(key crypto.Signer, input []byte) ([]byte, error) {
	switch key.Public().(type) {
	case ed25519.PublicKey:
		sig, err := key.Sign(rand.Reader, input, crypto.Hash(0))
		if err != nil {
			return nil, fmt.Errorf("limes: DPoP sign: %w", err)
		}
		return sig, nil
	case *ecdsa.PublicKey:
		digest := sha256.Sum256(input)
		der, err := key.Sign(rand.Reader, digest[:], crypto.SHA256)
		if err != nil {
			return nil, fmt.Errorf("limes: DPoP sign: %w", err)
		}
		var rs struct{ R, S *big.Int }
		if rest, err := asn1.Unmarshal(der, &rs); err != nil || len(rest) > 0 {
			return nil, errors.New("limes: DPoP sign: malformed ECDSA signature")
		}
		sig := make([]byte, es256SignatureBytes)
		rs.R.FillBytes(sig[:p256CoordinateBytes])
		rs.S.FillBytes(sig[p256CoordinateBytes:])
		return sig, nil
	}
	return nil, fmt.Errorf("limes: unsupported DPoP key type %T", key.Public())
}

func verifyDPoPSignature(pub crypto.PublicKey, alg string, input, sig []byte) bool {
	switch k := pub.(type) {
	case ed25519.PublicKey:
		return alg == algEdDSA && ed25519.Verify(k, input, sig)
	case *ecdsa.PublicKey:
		if alg != algES256 || len(sig) != es256SignatureBytes {
			return false
		}
		digest := sha256.Sum256(input)
		r := new(big.Int).SetBytes(sig[:p256CoordinateBytes])
		s := new(big.Int).SetBytes(sig[p256CoordinateBytes:])
		return ecdsa.Verify(k, digest[:], r, s)
	}
	return false
}

// canonicalHTU is the URL as RFC 9449 compares it: scheme, host and path,
// without query or fragment.
func canonicalHTU(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("limes: DPoP htu: %w", err)
	}
	u.RawQuery, u.Fragment, u.RawFragment = "", "", ""
	return u.String(), nil
}

// DPoPRequest is the request a proof must match.
type DPoPRequest struct {
	// Proof is the DPoP header's value; empty when the request had none.
	Proof string
	// Method is the HTTP method, e.g. "POST".
	Method string
	// URL is the request URL. When MatchPathOnly is set, only its path is
	// compared, and the proof's path may carry extra leading segments — for
	// a server behind a proxy, which cannot know the scheme, host or mount
	// prefix its clients address it by.
	URL           string
	MatchPathOnly bool
	// Token is the capability token the request presents.
	Token string
}

// ReplayCache remembers proof ids for as long as a proof could be accepted.
type ReplayCache interface {
	// Seen records jti until expires and reports whether it was already
	// recorded. An implementation that cannot tell — its store is
	// unreachable — reports true: an unanswerable check refuses the proof.
	Seen(ctx context.Context, jti string, expires time.Time) bool
}

// DPoPVerifier checks proofs for key-bound capabilities.
type DPoPVerifier struct {
	// Window bounds |now − iat|. Default DefaultDPoPWindow.
	Window time.Duration
	// Replay refuses a jti seen before. Required: without it a captured
	// proof is good for the whole window.
	Replay ReplayCache
	// Now is the clock; default time.Now.
	Now func() time.Time
}

// Check verifies req's proof against the capability. A capability that is
// not key-bound needs no proof and passes. A key-bound one passes only with a
// proof that: is a dpop+jwt signed by the key its header carries; whose key's
// thumbprint is the capability's; that names this method and URL and this
// token's hash; that was issued within the window; and whose jti is new.
func (v *DPoPVerifier) Check(ctx context.Context, c Capability, req DPoPRequest) error {
	if c.ConfirmationJKT == "" {
		return nil
	}
	if req.Proof == "" {
		return ErrDPoPRequired
	}
	window := v.Window
	if window <= 0 {
		window = DefaultDPoPWindow
	}
	now := time.Now
	if v.Now != nil {
		now = v.Now
	}

	parts, err := splitToken(req.Proof)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrDPoPInvalid, err)
	}
	var header dpopHeader
	if err := decodeSegment(parts.header, &header); err != nil || header.Typ != DPoPTokenType || header.JWK == nil {
		return fmt.Errorf("%w: header", ErrDPoPInvalid)
	}
	thumb, err := header.JWK.Thumbprint()
	if err != nil || thumb != c.ConfirmationJKT {
		return fmt.Errorf("%w: key is not the one the capability is bound to", ErrDPoPInvalid)
	}
	pub, err := header.JWK.publicKey()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrDPoPInvalid, err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts.sig)
	if err != nil || !verifyDPoPSignature(pub, header.Alg, []byte(parts.signingInput()), sig) {
		return fmt.Errorf("%w: signature", ErrDPoPInvalid)
	}

	var claims dpopClaims
	if err := decodeSegment(parts.claims, &claims); err != nil || claims.JTI == "" {
		return fmt.Errorf("%w: claims", ErrDPoPInvalid)
	}
	if !strings.EqualFold(claims.HTM, req.Method) {
		return fmt.Errorf("%w: method %q, proof names %q", ErrDPoPInvalid, req.Method, claims.HTM)
	}
	if !htuMatches(claims.HTU, req.URL, req.MatchPathOnly) {
		return fmt.Errorf("%w: URL does not match the proof's htu", ErrDPoPInvalid)
	}
	if claims.ATH != AccessTokenHash(req.Token) {
		return fmt.Errorf("%w: proof is for another token", ErrDPoPInvalid)
	}
	iat := time.Unix(claims.IAT, 0)
	if d := now().Sub(iat); d > window || d < -window {
		return fmt.Errorf("%w: issued at %s, outside the %s window", ErrDPoPInvalid, iat.UTC(), window)
	}
	if v.Replay == nil {
		return fmt.Errorf("%w: no replay cache configured", ErrDPoPInvalid)
	}
	if v.Replay.Seen(ctx, claims.JTI, iat.Add(window)) {
		return ErrDPoPReplayed
	}
	return nil
}

func htuMatches(htu, requestURL string, pathOnly bool) bool {
	want, err := url.Parse(requestURL)
	if err != nil {
		return false
	}
	got, err := url.Parse(htu)
	if err != nil {
		return false
	}
	if pathOnly {
		// A proxy may mount the service under a prefix it strips before
		// forwarding, so the client's path may carry segments the server
		// never sees. The server's path must still be the whole tail.
		return got.Path == want.Path ||
			(strings.HasPrefix(want.Path, "/") && strings.HasSuffix(got.Path, want.Path))
	}
	if got.Path != want.Path {
		return false
	}
	return strings.EqualFold(got.Scheme, want.Scheme) && strings.EqualFold(got.Host, want.Host)
}

func decodeSegment(seg string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// MemoryReplayCache is a bounded in-process ReplayCache. A deployment with
// several replicas behind a balancer that does not pin a client to one
// replica can see a proof replayed on another replica within the window;
// keep the window short, or back ReplayCache with a shared store.
type MemoryReplayCache struct {
	mu         sync.Mutex
	seen       map[string]time.Time
	expiries   expiryHeap // the ids in seen, soonest to expire first
	maxEntries int
	now        func() time.Time
}

// NewMemoryReplayCache bounds the cache at maxEntries (≤ 0: 100 000).
func NewMemoryReplayCache(maxEntries int) *MemoryReplayCache {
	if maxEntries <= 0 {
		maxEntries = defaultReplayCacheEntries
	}
	return &MemoryReplayCache{seen: map[string]time.Time{}, maxEntries: maxEntries, now: time.Now}
}

// Seen implements ReplayCache. Expired ids are dropped as they lapse, soonest
// first, so a full cache costs a lookup rather than a scan. When it is full
// of live ids, the new id is refused as a replay rather than accepted
// unrecorded — a full cache errs towards refusing.
func (c *MemoryReplayCache) Seen(_ context.Context, jti string, expires time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for len(c.expiries) > 0 && !c.expiries[0].expires.After(now) {
		delete(c.seen, heap.Pop(&c.expiries).(replayEntry).jti)
	}
	if _, ok := c.seen[jti]; ok {
		return true
	}
	if len(c.seen) >= c.maxEntries {
		return true
	}
	c.seen[jti] = expires
	heap.Push(&c.expiries, replayEntry{jti: jti, expires: expires})
	return false
}

// replayEntry is one id a MemoryReplayCache holds, and when it lapses.
type replayEntry struct {
	jti     string
	expires time.Time
}

// expiryHeap is a min-heap of replayEntry by expiry (container/heap).
type expiryHeap []replayEntry

func (h expiryHeap) Len() int           { return len(h) }
func (h expiryHeap) Less(i, j int) bool { return h[i].expires.Before(h[j].expires) }
func (h expiryHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *expiryHeap) Push(x any)        { *h = append(*h, x.(replayEntry)) }
func (h *expiryHeap) Pop() any {
	old := *h
	e := old[len(old)-1]
	*h = old[:len(old)-1]
	return e
}

// validateThumbprint accepts "" (unbound) or a base64url SHA-256 thumbprint.
// A malformed binding could match no key, minting a capability nobody can use.
func validateThumbprint(jkt string) error {
	if jkt == "" {
		return nil
	}
	if raw, err := base64.RawURLEncoding.DecodeString(jkt); err != nil || len(raw) != sha256.Size {
		return invalidRequest("ConfirmationJKT %q is not a base64url SHA-256 thumbprint", jkt)
	}
	return nil
}
