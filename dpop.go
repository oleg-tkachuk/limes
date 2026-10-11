package limes

import (
	"container/heap"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"

	"github.com/oleg-tkachuk/limes/internal/jose"
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

const (
	// es256SignatureBytes is a JWS ES256 signature: r || s, not DER.
	es256SignatureBytes = 2 * jose.P256CoordinateBytes
	// dpopJTIBytes is the randomness in a proof's jti.
	dpopJTIBytes = 16
	// defaultReplayCacheEntries bounds a MemoryReplayCache built with no
	// explicit bound.
	defaultReplayCacheEntries = 100_000
)

// Proof-of-possession rejections. Each matches ErrProofOfPossession, and so
// ErrUnauthenticated: the token may be genuine, the proof that its holder sent
// it is what failed.
var (
	// ErrProofOfPossession is the family of every proof-of-possession
	// refusal.
	ErrProofOfPossession error = &familyError{"limes: proof of possession refused", ErrUnauthenticated}
	// ErrDPoPRequired — the capability is key-bound and no proof came with it.
	ErrDPoPRequired error = &familyError{"limes: DPoP proof required", ErrProofOfPossession}
	// ErrDPoPInvalid — a proof came, and it does not prove possession for
	// this token, method, URL and moment.
	ErrDPoPInvalid error = &familyError{"limes: invalid DPoP proof", ErrProofOfPossession}
	// ErrDPoPReplayed — a proof's jti was already used inside the window.
	ErrDPoPReplayed error = &familyError{"limes: DPoP proof replayed", ErrProofOfPossession}
)

type dpopHeader struct {
	Typ string   `json:"typ"`
	Alg string   `json:"alg"`
	JWK *DPoPJWK `json:"jwk"`
}

// jwkPrivateMembers are the JWK members that carry private key material
// (RFC 7518 §6.2.2, §6.3.2, §6.4; RFC 8037 §2): "d" for EC and OKP keys, the
// RSA CRT members, and "k" for a symmetric key.
var jwkPrivateMembers = []string{"d", "p", "q", "dp", "dq", "qi", "oth", "k"}

// jwkHasPrivateMember reports whether the header segment's jwk carries any
// private key member — which DPoPJWK, holding the public members alone, would
// silently drop.
func jwkHasPrivateMember(headerSeg string) bool {
	var h struct {
		JWK map[string]jsontext.Value `json:"jwk"`
	}
	if decodeSegment(headerSeg, &h) != nil {
		return true // unreadable: refuse rather than assume
	}
	return slices.ContainsFunc(jwkPrivateMembers, func(m string) bool {
		_, ok := h.JWK[m]
		return ok
	})
}

type dpopClaims struct {
	JTI string  `json:"jti"`
	HTM string  `json:"htm"`
	HTU string  `json:"htu"`
	IAT float64 `json:"iat"` // NumericDate: seconds, possibly fractional (RFC 7519 §2)
	ATH string  `json:"ath,omitempty"`
	// Nonce is the server-provided nonce (RFC 9449 §8), when one was asked.
	Nonce string `json:"nonce,omitempty"`
}

// DPoPJWK is the public key a proof carries in its header: an Ed25519 (OKP)
// or P-256 (EC) key.
type DPoPJWK = jose.JWK

// PublicJWK describes pub, an ed25519.PublicKey or a P-256 *ecdsa.PublicKey,
// as a JWK.
func PublicJWK(pub crypto.PublicKey) (DPoPJWK, error) {
	j, err := jose.PublicJWK(pub)
	if err != nil {
		return DPoPJWK{}, fmt.Errorf("limes: DPoP key: %w", err)
	}
	return j, nil
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
	return jose.B64(sum[:])
}

// DPoPProofRequest is the request a proof is signed for.
type DPoPProofRequest struct {
	// Method and URL are the request's, the method exactly as sent: it is
	// case-sensitive (RFC 9110 §9.1).
	Method string
	URL    string
	// Token is the capability the request presents.
	Token string
	// Nonce is the last DPoP-Nonce the server sent, empty before it has
	// sent one.
	Nonce string
	// Now is the proof's iat.
	Now time.Time
}

// NewDPoPProof signs a proof for one request. key is any crypto.Signer whose
// public key is Ed25519 or ECDSA P-256.
func NewDPoPProof(key crypto.Signer, req DPoPProofRequest) (string, error) {
	jwk, err := PublicJWK(key.Public())
	if err != nil {
		return "", err
	}
	alg := jose.AlgEd25519
	if jwk.Kty == jose.KtyEC {
		alg = jose.AlgES256
	}
	jti := make([]byte, dpopJTIBytes)
	if _, err := rand.Read(jti); err != nil {
		return "", fmt.Errorf("limes: DPoP jti: %w", err)
	}
	htu, err := canonicalHTU(req.URL)
	if err != nil {
		return "", err
	}
	header, _ := json.Marshal(dpopHeader{Typ: DPoPTokenType, Alg: alg, JWK: &jwk})
	claims, _ := json.Marshal(dpopClaims{
		JTI: jose.B64(jti), HTM: req.Method, HTU: htu, IAT: float64(req.Now.Unix()),
		ATH: AccessTokenHash(req.Token), Nonce: req.Nonce,
	})
	input := jose.B64(header) + "." + jose.B64(claims)
	sig, err := signDPoP(key, []byte(input))
	if err != nil {
		return "", err
	}
	return input + "." + jose.B64(sig), nil
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
		rs.R.FillBytes(sig[:jose.P256CoordinateBytes])
		rs.S.FillBytes(sig[jose.P256CoordinateBytes:])
		return sig, nil
	}
	return nil, fmt.Errorf("limes: unsupported DPoP key type %T", key.Public())
}

func verifyDPoPSignature(pub crypto.PublicKey, alg string, input, sig []byte) bool {
	switch k := pub.(type) {
	case ed25519.PublicKey:
		return (alg == jose.AlgEd25519 || alg == jose.AlgEdDSA) && ed25519.Verify(k, input, sig)
	case *ecdsa.PublicKey:
		if alg != jose.AlgES256 || len(sig) != es256SignatureBytes {
			return false
		}
		digest := sha256.Sum256(input)
		r := new(big.Int).SetBytes(sig[:jose.P256CoordinateBytes])
		s := new(big.Int).SetBytes(sig[jose.P256CoordinateBytes:])
		return ecdsa.Verify(k, digest[:], r, s)
	}
	return false
}

// canonicalHTU is the htu a proof carries for rawURL: the URI as RFC 9449
// compares it (normalizeURI), without query or fragment.
func canonicalHTU(raw string) (string, error) {
	htu, err := normalizeURI(raw)
	if err != nil {
		return "", fmt.Errorf("limes: DPoP htu: %w", err)
	}
	return htu, nil
}

// DPoPProof is a request's proof of possession and the request it must
// match: what VerifyRequest.DPoP carries for a key-bound capability.
type DPoPProof struct {
	// Proof is the DPoP header's value; empty when the request had none.
	Proof string
	// Method is the HTTP method, e.g. "POST".
	Method string
	// URL is the request URL, as the server saw it (see
	// DPoPVerifier.MatchPathOnly).
	URL string
}

// ReplayCache remembers proof ids for as long as a proof could be accepted.
type ReplayCache interface {
	// Seen records the proof id jti of the key with thumbprint jkt until
	// expires, and reports whether it was already recorded. An
	// implementation that cannot tell — its store is unreachable, or the
	// key has used its share of the store — reports true: an unanswerable
	// check refuses the proof.
	Seen(ctx context.Context, jkt, jti string, expires time.Time) bool
}

// DPoPVerifier is how a StandardVerifier checks the proofs of key-bound
// capabilities (VerifierConfig.DPoP).
type DPoPVerifier struct {
	// Window bounds |now − iat|. Default DefaultDPoPWindow.
	Window time.Duration
	// Replay refuses a jti seen before. Required: without it a captured
	// proof is good for the whole window, and NewStandardVerifier refuses
	// the configuration.
	Replay ReplayCache
	// Now is the clock; default the verifier's.
	Now func() time.Time
	// Nonces, when set, requires every proof to carry a nonce it issued
	// (RFC 9449 §8); a proof without one is refused with
	// ErrDPoPNonceRequired, and StandardVerifier.DPoPNonce gives the nonce
	// to send back.
	Nonces DPoPNonces
	// MatchPathOnly compares only the path of DPoPProof.URL, and lets the
	// proof's path carry extra leading segments — for a server behind a
	// proxy, which cannot know the scheme, host or mount prefix its clients
	// address it by.
	MatchPathOnly bool
}

// DPoPSigningAlgorithms are the JWS algorithms a DPoP proof may be signed
// with, as a challenge advertises them (RFC 9449 §7.1): the fully-specified
// names (RFC 9864). The deprecated "EdDSA" is still read, never advertised.
func DPoPSigningAlgorithms() []string { return []string{jose.AlgEd25519, jose.AlgES256} }

// errNoReplayCache is a DPoPVerifier configured without the replay cache it
// requires.
var errNoReplayCache = errors.New("limes: DPoPVerifier.Replay required")

// check verifies p against the key-bound capability c presented as token. It
// passes only with a proof that: is a dpop+jwt signed by the key its header
// carries; whose key's thumbprint is the capability's; that names this method
// and URL and this token's hash; that was issued within the window; and whose
// jti is new.
func (v *DPoPVerifier) check(ctx context.Context, c Capability, token string, p DPoPProof) error {
	if p.Proof == "" {
		return ErrDPoPRequired
	}
	window := v.Window
	if window <= 0 {
		window = DefaultDPoPWindow
	}
	now := v.Now

	parts, err := jose.Split(p.Proof)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrDPoPInvalid, err)
	}
	var header dpopHeader
	if err := decodeSegment(parts.Header, &header); err != nil || header.Typ != DPoPTokenType || header.JWK == nil {
		return fmt.Errorf("%w: header", ErrDPoPInvalid)
	}
	// RFC 9449 §4.3: the jwk must not contain a private key.
	if jwkHasPrivateMember(parts.Header) {
		return fmt.Errorf("%w: jwk carries a private key", ErrDPoPInvalid)
	}
	thumb, err := header.JWK.Thumbprint()
	if err != nil || thumb != c.ConfirmationJKT {
		return fmt.Errorf("%w: key is not the one the capability is bound to", ErrDPoPInvalid)
	}
	pub, err := header.JWK.PublicKey()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrDPoPInvalid, err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts.Signature)
	if err != nil || !verifyDPoPSignature(pub, header.Alg, []byte(parts.SigningInput()), sig) {
		return fmt.Errorf("%w: signature", ErrDPoPInvalid)
	}

	var claims dpopClaims
	if err := decodeSegment(parts.Payload, &claims); err != nil || claims.JTI == "" {
		return fmt.Errorf("%w: claims", ErrDPoPInvalid)
	}
	// RFC 9110 §9.1: a method is case-sensitive.
	if claims.HTM != p.Method {
		return fmt.Errorf("%w: method %q, proof names %q", ErrDPoPInvalid, p.Method, claims.HTM)
	}
	if !htuMatches(claims.HTU, p.URL, v.MatchPathOnly) {
		return fmt.Errorf("%w: URL does not match the proof's htu", ErrDPoPInvalid)
	}
	if claims.ATH != AccessTokenHash(token) {
		return fmt.Errorf("%w: proof is for another token", ErrDPoPInvalid)
	}
	if math.IsNaN(claims.IAT) || math.IsInf(claims.IAT, 0) || math.Abs(claims.IAT) > MaxIJSONInteger {
		return fmt.Errorf("%w: iat %v", ErrDPoPInvalid, claims.IAT)
	}
	sec, frac := math.Modf(claims.IAT)
	iat := time.Unix(int64(sec), int64(frac*float64(time.Second)))
	if d := now().Sub(iat); d > window || d < -window {
		return fmt.Errorf("%w: issued at %s, outside the %s window", ErrDPoPInvalid, iat.UTC(), window)
	}
	if v.Nonces != nil && !v.Nonces.Valid(ctx, claims.Nonce) {
		return ErrDPoPNonceRequired
	}
	if v.Replay.Seen(ctx, thumb, claims.JTI, iat.Add(window)) {
		return ErrDPoPReplayed
	}
	return nil
}

// htuMatches compares a proof's htu with the request URL after RFC 3986
// syntax- and scheme-based normalisation, as RFC 9449 §4.3 requires. Under
// pathOnly it compares the normalised paths alone: the htu must still be an
// absolute URI, and the request URL must have a path.
func htuMatches(htu, requestURL string, pathOnly bool) bool {
	if pathOnly {
		// The proof's htu is still a URI a request could have been made to:
		// absolute, with a host, normalised.
		normalised, err := normalizeURI(htu)
		if err != nil {
			return false
		}
		got, err1 := url.Parse(normalised)
		want, err2 := url.Parse(requestURL)
		if err1 != nil || err2 != nil || want.EscapedPath() == "" {
			return false
		}
		g, w := got.EscapedPath(), normalizePath(want.EscapedPath())
		// A proxy may mount the service under a prefix it strips before
		// forwarding, so the client's path may carry segments the server
		// never sees. The server's path must still be the whole tail, from a
		// segment boundary.
		return g == w || strings.HasSuffix(g, w) && strings.HasPrefix(w, "/")
	}
	got, err1 := normalizeURI(htu)
	want, err2 := normalizeURI(requestURL)
	return err1 == nil && err2 == nil && got == want
}

func decodeSegment(seg string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

// MemoryReplayCache is a bounded in-process ReplayCache. A deployment with
// several replicas behind a balancer that does not pin a client to one
// replica can see a proof replayed on another replica within the window;
// keep the window short, use server nonces (DPoPVerifier.Nonces), or back
// ReplayCache with a shared store.
//
// Every key gets a share of the cache of its own (WithEntriesPerKey), so the
// holder of one key-bound token cannot fill it with proofs and lock every
// other key out: past its share, only that key's proofs are refused.
type MemoryReplayCache struct {
	mu         sync.Mutex
	seen       map[replayKey]time.Time
	perKey     map[string]int // live ids per key thumbprint
	expiries   expiryHeap     // the ids in seen, soonest to expire first
	maxEntries int
	maxPerKey  int
	now        func() time.Time
}

// replayKey is one proof id of one key.
type replayKey struct{ jkt, jti string }

// DefaultReplayEntriesPerKey is how many live proof ids one key may hold in
// a MemoryReplayCache: a thousand requests a window from one key.
const DefaultReplayEntriesPerKey = 1_000

// ReplayCacheOption configures a MemoryReplayCache.
type ReplayCacheOption func(*MemoryReplayCache)

// WithEntriesPerKey bounds the live proof ids of one key; ≤ 0 keeps the
// default. It is never more than the whole cache.
func WithEntriesPerKey(n int) ReplayCacheOption {
	return func(c *MemoryReplayCache) {
		if n > 0 {
			c.maxPerKey = n
		}
	}
}

// NewMemoryReplayCache bounds the cache at maxEntries (≤ 0: 100 000), and
// each key at DefaultReplayEntriesPerKey unless an option says otherwise.
func NewMemoryReplayCache(maxEntries int, opts ...ReplayCacheOption) *MemoryReplayCache {
	if maxEntries <= 0 {
		maxEntries = defaultReplayCacheEntries
	}
	c := &MemoryReplayCache{
		seen: map[replayKey]time.Time{}, perKey: map[string]int{},
		maxEntries: maxEntries, maxPerKey: DefaultReplayEntriesPerKey, now: time.Now,
	}
	for _, o := range opts {
		o(c)
	}
	c.maxPerKey = min(c.maxPerKey, c.maxEntries)
	return c
}

// Seen implements ReplayCache. Expired ids are dropped as they lapse, soonest
// first, so a full cache costs a lookup rather than a scan. A key past its
// share, or a cache full of live ids, refuses the new id as a replay rather
// than accept it unrecorded — a full cache errs towards refusing.
func (c *MemoryReplayCache) Seen(_ context.Context, jkt, jti string, expires time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for len(c.expiries) > 0 && !c.expiries[0].expires.After(now) {
		k := heap.Pop(&c.expiries).(replayEntry).key
		delete(c.seen, k)
		if c.perKey[k.jkt]--; c.perKey[k.jkt] <= 0 {
			delete(c.perKey, k.jkt)
		}
	}
	k := replayKey{jkt: jkt, jti: jti}
	if _, ok := c.seen[k]; ok {
		return true
	}
	if c.perKey[jkt] >= c.maxPerKey || len(c.seen) >= c.maxEntries {
		return true
	}
	c.seen[k] = expires
	c.perKey[jkt]++
	heap.Push(&c.expiries, replayEntry{key: k, expires: expires})
	return false
}

// replayEntry is one id a MemoryReplayCache holds, and when it lapses.
type replayEntry struct {
	key     replayKey
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
