// Package dpop implements proof of possession after RFC 9449 (DPoP): a
// client signs a short JWT over each request with a key it holds, and a
// server checks the proof against the key a token is bound to. It depends on
// no token format: the server says which thumbprint the key must have and
// which access token the proof must name.
package dpop

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
	"github.com/oleg-tkachuk/limes/internal/rfc3986"
)

// Proof of possession, after RFC 9449 (DPoP).
//
// A capability may be bound to a key its holder keeps: the token then carries
// the key's RFC 7638 thumbprint in `cnf.jkt`, and every request that presents
// it must also carry a DPoP proof — a short JWT signed with that key over the
// request's method and URL, the hash of the token, a unique id and the time.
// A token copied out of a prompt or a log is useless without the key.

// Header is the HTTP header a proof travels in.
const Header = "DPoP"

// tokenType is the JWT "typ" of a proof.
const tokenType = "dpop+jwt"

// DefaultWindow is how far a proof's iat may be from the verifier's clock.
const DefaultWindow = time.Minute

// ReplayClockSkew is how much longer than its acceptance window a proof's id
// is remembered (ReplayEntry.ExpiresAt): a replay cache reads its own clock,
// a moment after the verifier — or, shared, a different machine's — and must
// still hold the id of every proof the verifier could accept.
const ReplayClockSkew = 5 * time.Second

const (
	// es256SignatureBytes is a JWS ES256 signature: r || s, not DER.
	es256SignatureBytes = 2 * jose.P256CoordinateBytes
	// dpopJTIBytes is the randomness in a proof's jti.
	dpopJTIBytes = 16
)

// The refusals of a proof. A caller that judges what they mean for the
// request — limes reads each as a refusal of the credentials — wraps them;
// any other error Verify returns is a failure of the replay cache or the
// nonces, not of the proof.
var (
	// ErrRequired — the token is key-bound and no proof came with it.
	ErrRequired = errors.New("dpop: proof required")
	// ErrInvalid — a proof came, and it does not prove possession for this
	// token, method, URL and moment.
	ErrInvalid = errors.New("dpop: invalid proof")
	// ErrReplayed — a proof's jti was already used inside the window.
	ErrReplayed = errors.New("dpop: proof replayed")
)

type proofHeader struct {
	Typ string    `json:"typ"`
	Alg string    `json:"alg"`
	JWK *jose.JWK `json:"jwk"`
}

// jwkPrivateMembers are the JWK members that carry private key material
// (RFC 7518 §6.2.2, §6.3.2, §6.4; RFC 8037 §2): "d" for EC and OKP keys, the
// RSA CRT members, and "k" for a symmetric key.
var jwkPrivateMembers = []string{"d", "p", "q", "dp", "dq", "qi", "oth", "k"}

// jwkHasPrivateMember reports whether the header segment's jwk carries any
// private key member — which jose.JWK, holding the public members alone, would
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

type proofClaims struct {
	JTI string  `json:"jti"`
	HTM string  `json:"htm"`
	HTU string  `json:"htu"`
	IAT float64 `json:"iat"` // NumericDate: seconds, possibly fractional (RFC 7519 §2)
	ATH string  `json:"ath,omitempty"`
	// Nonce is the server-provided nonce (RFC 9449 §8), when one was asked.
	Nonce string `json:"nonce,omitempty"`
}

// publicJWK describes pub, an ed25519.PublicKey or a P-256 *ecdsa.PublicKey,
// as a JWK.
func publicJWK(pub crypto.PublicKey) (jose.JWK, error) {
	j, err := jose.PublicJWK(pub)
	if err != nil {
		return jose.JWK{}, fmt.Errorf("dpop: key: %w", err)
	}
	return j, nil
}

// Thumbprint is the RFC 7638 SHA-256 thumbprint of pub's JWK, base64url:
// what to put in an IssueRequest's ConfirmationJKT to bind a capability to
// the holder of pub.
func Thumbprint(pub crypto.PublicKey) (string, error) {
	j, err := publicJWK(pub)
	if err != nil {
		return "", err
	}
	return j.Thumbprint()
}

// accessTokenHash is the proof's `ath`: base64url(SHA-256(token)).
func accessTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return jose.B64(sum[:])
}

// ProofRequest is the request a proof is signed for.
type ProofRequest struct {
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

// Sign signs a proof for one request. key is any crypto.Signer whose
// public key is Ed25519 or ECDSA P-256.
func Sign(key crypto.Signer, req ProofRequest) (string, error) {
	jwk, err := publicJWK(key.Public())
	if err != nil {
		return "", err
	}
	alg := jose.AlgEd25519
	if jwk.Kty == jose.KtyEC {
		alg = jose.AlgES256
	}
	jti := make([]byte, dpopJTIBytes)
	if _, err := rand.Read(jti); err != nil {
		return "", fmt.Errorf("dpop: jti: %w", err)
	}
	htu, err := canonicalHTU(req.URL)
	if err != nil {
		return "", err
	}
	header, _ := json.Marshal(proofHeader{Typ: tokenType, Alg: alg, JWK: &jwk})
	claims, _ := json.Marshal(proofClaims{
		JTI: jose.B64(jti), HTM: req.Method, HTU: htu, IAT: float64(req.Now.Unix()),
		ATH: accessTokenHash(req.Token), Nonce: req.Nonce,
	})
	input := jose.B64(header) + "." + jose.B64(claims)
	sig, err := signProof(key, []byte(input))
	if err != nil {
		return "", err
	}
	return input + "." + jose.B64(sig), nil
}

// signProof signs through the crypto.Signer interface alone, so a key held
// in a KMS or an HSM works as well as one in memory.
func signProof(key crypto.Signer, input []byte) ([]byte, error) {
	switch key.Public().(type) {
	case ed25519.PublicKey:
		sig, err := key.Sign(rand.Reader, input, crypto.Hash(0))
		if err != nil {
			return nil, fmt.Errorf("dpop: sign: %w", err)
		}
		return sig, nil
	case *ecdsa.PublicKey:
		digest := sha256.Sum256(input)
		der, err := key.Sign(rand.Reader, digest[:], crypto.SHA256)
		if err != nil {
			return nil, fmt.Errorf("dpop: sign: %w", err)
		}
		var rs struct{ R, S *big.Int }
		if rest, err := asn1.Unmarshal(der, &rs); err != nil || len(rest) > 0 {
			return nil, errors.New("dpop: sign: malformed ECDSA signature")
		}
		sig := make([]byte, es256SignatureBytes)
		rs.R.FillBytes(sig[:jose.P256CoordinateBytes])
		rs.S.FillBytes(sig[jose.P256CoordinateBytes:])
		return sig, nil
	}
	return nil, fmt.Errorf("dpop: unsupported DPoP key type %T", key.Public())
}

func verifySignature(pub crypto.PublicKey, alg string, input, sig []byte) bool {
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
// compares it (rfc3986.Normalize), without query or fragment.
func canonicalHTU(raw string) (string, error) {
	htu, err := rfc3986.Normalize(raw)
	if err != nil {
		return "", fmt.Errorf("dpop: htu: %w", err)
	}
	return htu, nil
}

// Proof is a request's proof of possession and the request it must match.
type Proof struct {
	// JWT is the DPoP header's value; empty when the request had none.
	JWT string
	// Method is the HTTP method, e.g. "POST".
	Method string
	// URL is the request URL, as the server saw it (see
	// VerifierConfig.MatchPathOnly).
	URL string
}

// Request is what Verify checks: a proof, the access token it must name, and
// the thumbprint of the key the token is bound to.
type Request struct {
	Proof Proof
	// Token is the access token the request presents.
	Token string
	// JKT is the RFC 7638 thumbprint of the key the token is bound to.
	JKT string
}

// ReplayEntry is one proof id to record: the id of a proof from the key
// with thumbprint JKT, remembered until ExpiresAt — ReplayClockSkew past the
// last instant the verifier accepts the proof.
type ReplayEntry struct {
	JKT, JTI  string
	ExpiresAt time.Time
}

// ReplayCache remembers proof ids for as long as a proof could be accepted.
type ReplayCache interface {
	// Seen records e and reports whether its id was already recorded. A key
	// that has used its share of the cache reports true: the proof is
	// refused rather than accepted unrecorded. A cache that cannot answer —
	// its store unreachable — returns an error, and Verify refuses the proof
	// with it.
	Seen(ctx context.Context, e ReplayEntry) (bool, error)
}

// VerifierConfig configures a Verifier. Zero fields take the defaults named
// on them; Replay is required.
type VerifierConfig struct {
	// Window bounds |now − iat|. Zero: DefaultWindow.
	Window time.Duration
	// Replay refuses a jti seen before. Required: without it a captured
	// proof is good for the whole window.
	Replay ReplayCache
	// Now is the clock. Nil: time.Now.
	Now func() time.Time
	// Nonces, when set, requires every proof to carry a nonce it issued
	// (RFC 9449 §8); a proof without one is refused with
	// ErrNonceRequired, and Nonce gives the nonce to send back.
	Nonces Nonces
	// MatchPathOnly compares only the path of Proof.URL, and lets the
	// proof's path carry extra leading segments — for a server behind a
	// proxy, which cannot know the scheme, host or mount prefix its clients
	// address it by.
	MatchPathOnly bool
}

// Verifier checks proofs against the key a token is bound to.
type Verifier struct {
	cfg VerifierConfig
}

// NewVerifier builds a Verifier from cfg, refusing one with no ReplayCache
// or a negative Window.
func NewVerifier(cfg VerifierConfig) (*Verifier, error) {
	if cfg.Replay == nil {
		return nil, ErrNoReplayCache
	}
	if cfg.Window < 0 {
		return nil, fmt.Errorf("dpop: Window %s is negative", cfg.Window)
	}
	if cfg.Window == 0 {
		cfg.Window = DefaultWindow
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Verifier{cfg: cfg}, nil
}

// SigningAlgorithms are the JWS algorithms a DPoP proof may be signed
// with, as a challenge advertises them (RFC 9449 §7.1): the fully-specified
// names (RFC 9864). The deprecated "EdDSA" is still read, never advertised.
func SigningAlgorithms() []string { return []string{jose.AlgEd25519, jose.AlgES256} }

// ErrNoReplayCache is a Verifier with no ReplayCache: a captured proof
// would be good for its whole window.
var ErrNoReplayCache = errors.New("dpop: Verifier.Replay required")

// Nonce is the nonce to send in the DPoP-Nonce header, empty when the
// verifier issues none.
func (v *Verifier) Nonce(ctx context.Context) (string, error) {
	if v.cfg.Nonces == nil {
		return "", nil
	}
	return v.cfg.Nonces.Nonce(ctx)
}

// check verifies p against the key-bound capability c presented as token. It
// passes only with a proof that: is a dpop+jwt signed by the key its header
// carries; whose key's thumbprint is the capability's; that names this method
// and URL and this token's hash; that was issued within the window; and whose
// jti is new.
func (v *Verifier) Verify(ctx context.Context, req Request) error {
	if v == nil || v.cfg.Replay == nil {
		return ErrNoReplayCache // a Verifier not made by NewVerifier
	}
	p, token := req.Proof, req.Token
	if p.JWT == "" {
		return ErrRequired
	}
	window, now := v.cfg.Window, v.cfg.Now

	parts, err := jose.Split(p.JWT)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	var header proofHeader
	if err := decodeSegment(parts.Header, &header); err != nil || header.Typ != tokenType || header.JWK == nil {
		return fmt.Errorf("%w: header", ErrInvalid)
	}
	// RFC 9449 §4.3: the jwk must not contain a private key.
	if jwkHasPrivateMember(parts.Header) {
		return fmt.Errorf("%w: jwk carries a private key", ErrInvalid)
	}
	thumb, err := header.JWK.Thumbprint()
	if err != nil || thumb != req.JKT {
		return fmt.Errorf("%w: key is not the one the capability is bound to", ErrInvalid)
	}
	pub, err := header.JWK.PublicKey()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts.Signature)
	if err != nil || !verifySignature(pub, header.Alg, []byte(parts.SigningInput()), sig) {
		return fmt.Errorf("%w: signature", ErrInvalid)
	}

	var claims proofClaims
	if err := decodeSegment(parts.Payload, &claims); err != nil || claims.JTI == "" {
		return fmt.Errorf("%w: claims", ErrInvalid)
	}
	// RFC 9110 §9.1: a method is case-sensitive.
	if claims.HTM != p.Method {
		return fmt.Errorf("%w: method %q, proof names %q", ErrInvalid, p.Method, claims.HTM)
	}
	if !htuMatches(claims.HTU, p.URL, v.cfg.MatchPathOnly) {
		return fmt.Errorf("%w: URL does not match the proof's htu", ErrInvalid)
	}
	if claims.ATH != accessTokenHash(token) {
		return fmt.Errorf("%w: proof is for another token", ErrInvalid)
	}
	if math.IsNaN(claims.IAT) || math.IsInf(claims.IAT, 0) || math.Abs(claims.IAT) > jose.MaxIJSONInteger {
		return fmt.Errorf("%w: iat %v", ErrInvalid, claims.IAT)
	}
	sec, frac := math.Modf(claims.IAT)
	iat := time.Unix(int64(sec), int64(frac*float64(time.Second)))
	if d := now().Sub(iat); d > window || d < -window {
		return fmt.Errorf("%w: issued at %s, outside the %s window", ErrInvalid, iat.UTC(), window)
	}
	if v.cfg.Nonces != nil {
		valid, err := v.cfg.Nonces.Valid(ctx, claims.Nonce)
		if err != nil {
			return fmt.Errorf("dpop: nonce check: %w", err)
		}
		if !valid {
			return ErrNonceRequired
		}
	}
	// Remembered past the last instant it is accepted, by more than the
	// cache's clock can lag the verifier's: at iat+window it is still
	// accepted, and a cache that dropped it then would let it through again.
	seen, err := v.cfg.Replay.Seen(ctx, ReplayEntry{JKT: thumb, JTI: claims.JTI, ExpiresAt: iat.Add(window).Add(ReplayClockSkew)})
	if err != nil {
		return fmt.Errorf("dpop: replay check: %w", err)
	}
	if seen {
		return ErrReplayed
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
		normalised, err := rfc3986.Normalize(htu)
		if err != nil {
			return false
		}
		got, err1 := url.Parse(normalised)
		want, err2 := url.Parse(requestURL)
		if err1 != nil || err2 != nil || want.EscapedPath() == "" {
			return false
		}
		g, w := got.EscapedPath(), rfc3986.NormalizePath(want.EscapedPath())
		// A proxy may mount the service under a prefix it strips before
		// forwarding, so the client's path may carry segments the server
		// never sees. The server's path must still be the whole tail, from a
		// segment boundary.
		return g == w || strings.HasSuffix(g, w) && strings.HasPrefix(w, "/")
	}
	got, err1 := rfc3986.Normalize(htu)
	want, err2 := rfc3986.Normalize(requestURL)
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
// keep the window short, use server nonces (VerifierConfig.Nonces), or back
// ReplayCache with a shared store.
//
// Every key gets a share of the cache of its own (EntriesPerKey), so the
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

// MemoryReplayCache defaults, for a field left zero.
const (
	// DefaultReplayCacheEntries bounds the whole cache.
	DefaultReplayCacheEntries = 100_000
	// DefaultReplayEntriesPerKey is how many live proof ids one key may
	// hold: a thousand requests a window from one key.
	DefaultReplayEntriesPerKey = 1_000
)

// MemoryReplayCacheConfig configures a MemoryReplayCache.
type MemoryReplayCacheConfig struct {
	// MaxEntries bounds the cache. Zero: DefaultReplayCacheEntries.
	MaxEntries int
	// EntriesPerKey bounds the live proof ids of one key, never more than
	// MaxEntries. Zero: DefaultReplayEntriesPerKey.
	EntriesPerKey int
	// Now is the clock. Nil: time.Now.
	Now func() time.Time
}

// NewMemoryReplayCache builds a MemoryReplayCache, refusing a negative bound.
func NewMemoryReplayCache(cfg MemoryReplayCacheConfig) (*MemoryReplayCache, error) {
	if cfg.MaxEntries < 0 || cfg.EntriesPerKey < 0 {
		return nil, fmt.Errorf("dpop: replay cache bounds %d, %d are negative", cfg.MaxEntries, cfg.EntriesPerKey)
	}
	if cfg.MaxEntries == 0 {
		cfg.MaxEntries = DefaultReplayCacheEntries
	}
	if cfg.EntriesPerKey == 0 {
		cfg.EntriesPerKey = DefaultReplayEntriesPerKey
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &MemoryReplayCache{
		seen: map[replayKey]time.Time{}, perKey: map[string]int{},
		maxEntries: cfg.MaxEntries, maxPerKey: min(cfg.EntriesPerKey, cfg.MaxEntries), now: cfg.Now,
	}, nil
}

// Seen implements ReplayCache. Expired ids are dropped as they lapse, soonest
// first, so a full cache costs a lookup rather than a scan. A key past its
// share, or a cache full of live ids, refuses the new id as a replay rather
// than accept it unrecorded — a full cache errs towards refusing.
func (c *MemoryReplayCache) Seen(_ context.Context, e ReplayEntry) (bool, error) {
	jkt, jti, expires := e.JKT, e.JTI, e.ExpiresAt
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
		return true, nil
	}
	if c.perKey[jkt] >= c.maxPerKey || len(c.seen) >= c.maxEntries {
		return true, nil
	}
	c.seen[k] = expires
	c.perKey[jkt]++
	heap.Push(&c.expiries, replayEntry{key: k, expires: expires})
	return false, nil
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

// IsThumbprint reports whether jkt is a base64url SHA-256 thumbprint, the
// form a key binding takes.
func IsThumbprint(jkt string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(jkt)
	return err == nil && len(raw) == sha256.Size
}
