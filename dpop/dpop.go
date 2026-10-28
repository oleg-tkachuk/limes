// Package dpop implements proof of possession after RFC 9449 (DPoP): a
// client signs a short JWT over each request with a key it holds, and a
// server checks the proof against the key a token is bound to. It depends on
// no token format: the server says which thumbprint the key must have and
// which access token the proof must name.
// An authorization server's token endpoint checks the proof a token request
// carries with CheckTokenRequest, and binds what it issues to the key that
// signed it (RFC 9449 §5).
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
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

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

// headerCrit is the JWS header parameter naming the extensions a recipient
// must understand (RFC 7515 §4.1.11).
const headerCrit = "crit"

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
// request — limes reads each as a refusal of the credentials — wraps them.
// Verify returns one of them, ErrNonceRequired, an error naming a Request it
// cannot check (no Holder), or a failure of the replay cache or the nonces,
// which is not a refusal of the proof.
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

// hasCriticalExtensions reports whether the header segment carries a crit
// member, whatever its value: one naming extensions this package cannot
// understand, or one RFC 7515 §4.1.11 forbids (empty, or not an array).
func hasCriticalExtensions(headerSeg string) bool {
	var h map[string]jsontext.Value
	if decodeSegment(headerSeg, &h) != nil {
		return true // unreadable: refuse rather than assume
	}
	_, ok := h[headerCrit]
	return ok
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

// SignRequest is the request a proof is signed for.
type SignRequest struct {
	// Method and URL are the request's, the method exactly as sent: it is
	// case-sensitive (RFC 9110 §9.1).
	Method string
	URL    string
	// Token is the capability the request presents. Empty for a request to
	// a token endpoint, which presents none: the proof then has no ath
	// (RFC 9449 §4.2, §5).
	Token string
	// Nonce is the last DPoP-Nonce the server sent, empty before it has
	// sent one.
	Nonce string
	// IssuedAt is the proof's iat. Required: a proof with no time is refused
	// rather than dated 1970.
	IssuedAt time.Time
}

// contextSigner is a crypto.Signer that signs under a context, as a key held
// in a KMS or an HSM does; limes.ContextSigner is one.
type contextSigner interface {
	SignContext(ctx context.Context, rand io.Reader, message []byte, opts crypto.SignerOpts) ([]byte, error)
}

// Sign signs a proof for one request. key is any crypto.Signer whose
// public key is Ed25519 or ECDSA P-256; one that also has SignContext is
// given ctx, so a slow or cancelled remote signature is bounded by it.
func Sign(ctx context.Context, key crypto.Signer, req SignRequest) (string, error) {
	if req.IssuedAt.IsZero() {
		return "", errors.New("dpop: SignRequest.IssuedAt required")
	}
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
	ath := ""
	if req.Token != "" {
		ath = accessTokenHash(req.Token)
	}
	header, _ := json.Marshal(proofHeader{Typ: tokenType, Alg: alg, JWK: &jwk})
	claims, _ := json.Marshal(proofClaims{
		JTI: jose.B64(jti), HTM: req.Method, HTU: htu, IAT: float64(req.IssuedAt.Unix()),
		ATH: ath, Nonce: req.Nonce,
	})
	input := jose.B64(header) + "." + jose.B64(claims)
	sig, err := signProof(ctx, key, []byte(input))
	if err != nil {
		return "", err
	}
	return input + "." + jose.B64(sig), nil
}

// signProof signs through the crypto.Signer interface alone, so a key held
// in a KMS or an HSM works as well as one in memory.
func signProof(ctx context.Context, key crypto.Signer, input []byte) ([]byte, error) {
	sign := key.Sign
	if cs, ok := key.(contextSigner); ok {
		sign = func(r io.Reader, m []byte, o crypto.SignerOpts) ([]byte, error) { return cs.SignContext(ctx, r, m, o) }
	}
	switch key.Public().(type) {
	case ed25519.PublicKey:
		sig, err := sign(rand.Reader, input, crypto.Hash(0))
		if err != nil {
			return nil, fmt.Errorf("dpop: sign: %w", err)
		}
		return sig, nil
	case *ecdsa.PublicKey:
		digest := sha256.Sum256(input)
		der, err := sign(rand.Reader, digest[:], crypto.SHA256)
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

// Request is what Verify checks: a proof, the access token it must name, the
// thumbprint of the key the token is bound to, and who holds it.
type Request struct {
	Proof Proof
	// Token is the access token the request presents.
	Token string
	// JKT is the RFC 7638 thumbprint of the key the token is bound to.
	JKT string
	// Holder names whose share of the replay cache the proof counts
	// against: the capability the token carries, however many keys its
	// copies are bound to. Required.
	Holder string
}

// ReplayEntry is one proof id to record: the id of a proof from the key
// with thumbprint JKT, counted against Holder's share, remembered until
// ExpiresAt — ReplayClockSkew past the last instant the verifier accepts the
// proof.
type ReplayEntry struct {
	JKT, JTI  string
	Holder    string
	ExpiresAt time.Time
}

// MaxJTIBytes bounds a proof's jti. RFC 9449 §4.2 asks for 96 bits of
// randomness, which a UUID or 22 base64url characters carry; a longer id
// would only cost the replay cache memory for the whole window.
const MaxJTIBytes = 128

// DefaultMaxProofBytes bounds a proof a Verifier will look at when its
// config names no MaxProofBytes. A proof with a P-256 key and a nonce is
// under a kilobyte.
const DefaultMaxProofBytes = 4 << 10

// ReplayCache remembers proof ids for as long as a proof could be accepted.
type ReplayCache interface {
	// Seen records e and reports whether its id was already recorded. A
	// holder that has used its share of the cache reports true: the proof is
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
	// MaxProofBytes bounds the proof, refused unread past it. Zero:
	// DefaultMaxProofBytes; negative is refused.
	MaxProofBytes int
	// MatchPathOnly compares only the path of Proof.URL, and lets the
	// proof's path carry extra leading segments — for a server behind a
	// proxy, which cannot know the scheme, host or mount prefix its clients
	// address it by. The root path "/" is the exception: it must match
	// exactly, since as a tail it would match every path that ends in a
	// slash, so a server mounted under a prefix cannot take proofs at its
	// root.
	MatchPathOnly bool
}

// Verifier checks proofs against the key a token is bound to.
type Verifier struct {
	cfg VerifierConfig
}

// NewVerifier builds a Verifier from cfg, refusing one with no ReplayCache,
// a negative Window or a negative MaxProofBytes.
func NewVerifier(cfg VerifierConfig) (*Verifier, error) {
	if cfg.Replay == nil {
		return nil, errNoReplayCache
	}
	if cfg.Window < 0 {
		return nil, fmt.Errorf("dpop: Window %s is negative", cfg.Window)
	}
	if cfg.MaxProofBytes < 0 {
		return nil, fmt.Errorf("dpop: MaxProofBytes %d is negative", cfg.MaxProofBytes)
	}
	if cfg.Window == 0 {
		cfg.Window = DefaultWindow
	}
	if cfg.MaxProofBytes == 0 {
		cfg.MaxProofBytes = DefaultMaxProofBytes
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

// errNoReplayCache is a Verifier with no ReplayCache: a captured proof
// would be good for its whole window.
var errNoReplayCache = errors.New("dpop: VerifierConfig.Replay required")

// Nonce is the nonce to send in the DPoP-Nonce header, empty when the
// verifier issues none.
func (v *Verifier) Nonce(ctx context.Context) (string, error) {
	if v.cfg.Nonces == nil {
		return "", nil
	}
	return v.cfg.Nonces.Nonce(ctx)
}

// Verify checks req.Proof against the key req.Token is bound to. It passes
// only with a proof that: is no longer than MaxProofBytes; is a dpop+jwt
// signed by the key its header carries; whose key's thumbprint is req.JKT;
// that names this method and URL and this token's hash; that was issued
// within the window; and whose jti, no longer than MaxJTIBytes, is new.
// It is Check followed by Record.
func (v *Verifier) Verify(ctx context.Context, req Request) error {
	checked, err := v.Check(ctx, req)
	if err != nil {
		return err
	}
	return v.Record(ctx, checked)
}

// CheckedProof is a proof Check accepted and Record has still to record.
// Only Check makes one.
type CheckedProof struct {
	entry ReplayEntry
}

// JKT is the RFC 7638 thumbprint of the key that signed the proof: after
// CheckTokenRequest, the key the issued token is to be bound to.
func (p CheckedProof) JKT() string { return p.entry.JKT }

// errNotChecked is a CheckedProof Check did not make.
var errNotChecked = errors.New("dpop: CheckedProof not made by Check")

// Check is everything Verify checks but whether the proof's jti is new: it
// records nothing in the replay cache. A caller with checks of its own to
// make between the two — a revocation lookup — runs them after Check and
// calls Record only once they pass, so a proof is recorded only for a
// request that is otherwise accepted, and the cheap refusals come first.
func (v *Verifier) Check(ctx context.Context, req Request) (CheckedProof, error) {
	if req.Token == "" {
		return CheckedProof{}, errors.New("dpop: Request.Token required")
	}
	if req.Holder == "" {
		return CheckedProof{}, errors.New("dpop: Request.Holder required")
	}
	return v.check(ctx, req.Proof, req.Holder, presented{token: req.Token, jkt: req.JKT})
}

// TokenRequest is what CheckTokenRequest checks: the proof an authorization
// server's token endpoint receives (RFC 9449 §5), which presents no access
// token, and whose key is the one the token it issues is to be bound to.
type TokenRequest struct {
	Proof Proof
	// Holder names whose share of the replay cache the proof counts
	// against: the client the token endpoint authenticated. Required.
	Holder string
}

// CheckTokenRequest is Check for a proof sent to a token endpoint: every
// check Check makes, but that the proof names no access token (no ath) and
// may be by any key. The CheckedProof's JKT is that key's thumbprint, to
// bind the issued token to; Record records the proof once the endpoint's
// own checks pass.
func (v *Verifier) CheckTokenRequest(ctx context.Context, req TokenRequest) (CheckedProof, error) {
	if req.Holder == "" {
		return CheckedProof{}, errors.New("dpop: TokenRequest.Holder required")
	}
	return v.check(ctx, req.Proof, req.Holder, presented{tokenRequest: true})
}

// presented is what the request a proof comes with presents: an access
// token bound to the key with thumbprint jkt, or, for a token request,
// neither, the proof's own key being the one to bind.
type presented struct {
	token, jkt   string
	tokenRequest bool
}

// check is Check and CheckTokenRequest: the proof p, counted against
// holder, for a request presenting what.
func (v *Verifier) check(ctx context.Context, p Proof, holder string, what presented) (CheckedProof, error) {
	if err := v.checkPresented(p); err != nil {
		return CheckedProof{}, err
	}
	thumb, claims, err := readProof(p.JWT, what)
	if err != nil {
		return CheckedProof{}, err
	}
	if err := checkForRequest(claims, p, what, v.cfg.MatchPathOnly); err != nil {
		return CheckedProof{}, err
	}
	window := v.cfg.Window
	iat, err := issuedWithin(claims.IAT, v.cfg.Now(), window)
	if err != nil {
		return CheckedProof{}, err
	}
	if err := v.checkNonce(ctx, claims.Nonce); err != nil {
		return CheckedProof{}, err
	}
	// Remembered past the last instant it is accepted, by more than the
	// cache's clock can lag the verifier's: at iat+window it is still
	// accepted, and a cache that dropped it then would let it through again.
	return CheckedProof{entry: ReplayEntry{
		JKT: thumb, JTI: claims.JTI, Holder: holder, ExpiresAt: iat.Add(window).Add(ReplayClockSkew),
	}}, nil
}

// checkPresented refuses a check by a Verifier NewVerifier did not make,
// no proof at all, or one past MaxProofBytes.
func (v *Verifier) checkPresented(p Proof) error {
	if v == nil || v.cfg.Replay == nil {
		return errNoReplayCache // a Verifier not made by NewVerifier
	}
	if p.JWT == "" {
		return ErrRequired
	}
	if len(p.JWT) > v.cfg.MaxProofBytes {
		return fmt.Errorf("%w: proof is %d bytes (limit %d)", ErrInvalid, len(p.JWT), v.cfg.MaxProofBytes)
	}
	return nil
}

// readProof reads a proof signed by the key its header carries, which must
// be the one what is bound to unless it is a token request's, and returns
// that key's thumbprint and the proof's claims.
func readProof(jwt string, what presented) (string, proofClaims, error) {
	parts, err := jose.Split(jwt)
	if err != nil {
		return "", proofClaims{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	header, err := readProofHeader(parts.Header)
	if err != nil {
		return "", proofClaims{}, err
	}
	thumb, pub, err := proofKey(header.JWK, what)
	if err != nil {
		return "", proofClaims{}, err
	}
	sig, err := jose.UnB64(parts.Signature)
	if err != nil || !verifySignature(pub, header.Alg, []byte(parts.SigningInput()), sig) {
		return "", proofClaims{}, fmt.Errorf("%w: signature", ErrInvalid)
	}
	var claims proofClaims
	if err := decodeSegment(parts.Payload, &claims); err != nil || claims.JTI == "" || len(claims.JTI) > MaxJTIBytes {
		return "", proofClaims{}, fmt.Errorf("%w: claims", ErrInvalid)
	}
	return thumb, claims, nil
}

// readProofHeader reads a proof's header: its type, and a public key.
func readProofHeader(seg string) (proofHeader, error) {
	var header proofHeader
	if err := decodeSegment(seg, &header); err != nil || header.Typ != tokenType || header.JWK == nil {
		return proofHeader{}, fmt.Errorf("%w: header", ErrInvalid)
	}
	// RFC 9449 §4.3: the jwk must not contain a private key.
	if jwkHasPrivateMember(seg) {
		return proofHeader{}, fmt.Errorf("%w: jwk carries a private key", ErrInvalid)
	}
	// RFC 7515 §4.1.11: an extension the recipient does not understand
	// makes the JWS invalid, and this package understands none.
	if hasCriticalExtensions(seg) {
		return proofHeader{}, fmt.Errorf("%w: header names critical extensions", ErrInvalid)
	}
	return header, nil
}

// proofKey is the thumbprint and public key of jwk, refused when it is not
// the key what is bound to, unless it is a token request's.
func proofKey(jwk *jose.JWK, what presented) (string, crypto.PublicKey, error) {
	thumb, err := jwk.Thumbprint()
	if err != nil {
		return "", nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if !what.tokenRequest && thumb != what.jkt {
		return "", nil, fmt.Errorf("%w: key is not the one the capability is bound to", ErrInvalid)
	}
	pub, err := jwk.PublicKey()
	if err != nil {
		return "", nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return thumb, pub, nil
}

// checkForRequest refuses a proof made for another request: another
// method, another URL, or another token than what presents.
func checkForRequest(claims proofClaims, p Proof, what presented, pathOnly bool) error {
	// RFC 9110 §9.1: a method is case-sensitive.
	if claims.HTM != p.Method {
		return fmt.Errorf("%w: method %q, proof names %q", ErrInvalid, p.Method, claims.HTM)
	}
	if !htuMatches(claims.HTU, p.URL, pathOnly) {
		return fmt.Errorf("%w: URL does not match the proof's htu", ErrInvalid)
	}
	// A token request presents no access token, so its proof names none
	// (RFC 9449 §4.2); one that does was made for a resource request.
	wantATH := ""
	if !what.tokenRequest {
		wantATH = accessTokenHash(what.token)
	}
	if claims.ATH != wantATH {
		return fmt.Errorf("%w: proof is for another token", ErrInvalid)
	}
	return nil
}

// issuedWithin is the proof's iat, refused when it is no finite I-JSON
// number or lies more than window either side of now.
func issuedWithin(iatClaim float64, now time.Time, window time.Duration) (time.Time, error) {
	if math.IsNaN(iatClaim) || math.IsInf(iatClaim, 0) || math.Abs(iatClaim) > jose.MaxIJSONInteger {
		return time.Time{}, fmt.Errorf("%w: iat %v", ErrInvalid, iatClaim)
	}
	sec, frac := math.Modf(iatClaim)
	iat := time.Unix(int64(sec), int64(frac*float64(time.Second)))
	if d := now.Sub(iat); d > window || d < -window {
		return time.Time{}, fmt.Errorf("%w: issued at %s, outside the %s window", ErrInvalid, iat.UTC(), window)
	}
	return iat, nil
}

// checkNonce refuses a proof without a nonce the server's Nonces accept,
// when it has any.
func (v *Verifier) checkNonce(ctx context.Context, nonce string) error {
	if v.cfg.Nonces == nil {
		return nil
	}
	valid, err := v.cfg.Nonces.Valid(ctx, nonce)
	if err != nil {
		return fmt.Errorf("dpop: nonce check: %w", err)
	}
	if !valid {
		return ErrNonceRequired
	}
	return nil
}

// Record records a proof Check accepted in the replay cache, refusing it
// with ErrReplayed when its jti was seen before.
func (v *Verifier) Record(ctx context.Context, p CheckedProof) error {
	if v == nil || v.cfg.Replay == nil {
		return errNoReplayCache // a Verifier not made by NewVerifier
	}
	if p.entry.JTI == "" {
		return errNotChecked
	}
	seen, err := v.cfg.Replay.Seen(ctx, p.entry)
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
// rootPath is the path of a server's root, and the separator a path starts
// with.
const rootPath = "/"

func htuMatches(htu, requestURL string, pathOnly bool) bool {
	if pathOnly {
		return htuPathMatches(htu, requestURL)
	}
	got, err1 := rfc3986.Normalize(htu)
	want, err2 := rfc3986.Normalize(requestURL)
	return err1 == nil && err2 == nil && got == want
}

// htuPathMatches is htuMatches under pathOnly: the proof's htu is still a
// URI a request could have been made to — absolute, with a host,
// normalised — and its path ends in the request's.
func htuPathMatches(htu, requestURL string) bool {
	normalised, err := rfc3986.Normalize(htu)
	if err != nil {
		return false
	}
	got, err1 := url.Parse(normalised)
	want, err2 := url.Parse(requestURL)
	if err1 != nil || err2 != nil || want.EscapedPath() == "" {
		return false
	}
	return pathTailMatches(got.EscapedPath(), rfc3986.NormalizePath(want.EscapedPath()))
}

// pathTailMatches reports whether the server's path w is g, or its whole
// tail from a segment boundary. A proxy may mount the service under a
// prefix it strips before forwarding, so the client's path may carry
// segments the server never sees. The tail must name a segment: the root's
// tail is every path that ends in a slash.
func pathTailMatches(g, w string) bool {
	return g == w || w != rootPath && strings.HasSuffix(g, w) && strings.HasPrefix(w, rootPath)
}

func decodeSegment(seg string, v any) error {
	raw, err := jose.UnB64(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

// MemoryReplayCache is a bounded in-process ReplayCache. A deployment with
// several replicas behind a balancer that does not pin a client to one
// replica can see a proof replayed on another replica within the window;
// keep the window short, use server nonces (VerifierConfig.Nonces), or back
// ReplayCache with a shared store — the redisreplay module ships one over
// Redis.
//
// Every holder gets a share of the cache of its own (EntriesPerHolder), so
// one capability cannot fill it with proofs and lock every other one out —
// not even a Biscuit whose copies are bound to as many keys as their holder
// likes, since they are one capability: past its share, only its proofs are
// refused. It does not stop one tenant holding many capabilities — Delegate
// mints them freely — from filling the cache with its proofs; past
// MaxEntries the cache refuses every new id rather than forget one, since a
// forgotten id could be replayed. Size MaxEntries for the busiest window.
// Server nonces (VerifierConfig.Nonces) do not bound it: an HMACNonces nonce
// may back any number of proofs until it expires, so they bound how early a
// proof is signed, not how many are.
//
// It keys a proof id by the key that signed it (jkt, jti) across holders,
// where redisreplay keys it within its holder: the same id under one key for
// two capabilities is refused here and accepted there. Neither is a replay —
// ath binds a proof to one token, and so to one holder.
type MemoryReplayCache struct {
	mu           sync.Mutex
	seen         map[replayKey]time.Time
	perHolder    map[string]int // live ids per holder
	expiries     expiryHeap     // the ids in seen, soonest to expire first
	maxEntries   int
	maxPerHolder int
	now          func() time.Time
}

// replayKey is one proof id of one key.
type replayKey struct{ jkt, jti string }

// MemoryReplayCache defaults, for a field left zero.
const (
	// DefaultReplayCacheEntries bounds the whole cache.
	DefaultReplayCacheEntries = 100_000
	// DefaultReplayEntriesPerHolder is how many live proof ids one holder
	// may have: a thousand requests a window from one capability.
	DefaultReplayEntriesPerHolder = 1_000
)

// MemoryReplayCacheConfig configures a MemoryReplayCache.
type MemoryReplayCacheConfig struct {
	// MaxEntries bounds the cache. Zero: DefaultReplayCacheEntries.
	MaxEntries int
	// EntriesPerHolder bounds the live proof ids of one holder, no more
	// than MaxEntries. Zero: DefaultReplayEntriesPerHolder, or MaxEntries
	// when that is smaller.
	EntriesPerHolder int
	// Now is the clock. Nil: time.Now.
	Now func() time.Time
}

// NewMemoryReplayCache builds a MemoryReplayCache, refusing a negative bound
// or a holder's share larger than the whole cache.
func NewMemoryReplayCache(cfg MemoryReplayCacheConfig) (*MemoryReplayCache, error) {
	if cfg.MaxEntries < 0 || cfg.EntriesPerHolder < 0 {
		return nil, fmt.Errorf("dpop: replay cache bounds %d, %d are negative", cfg.MaxEntries, cfg.EntriesPerHolder)
	}
	if cfg.MaxEntries == 0 {
		cfg.MaxEntries = DefaultReplayCacheEntries
	}
	switch {
	case cfg.EntriesPerHolder == 0:
		cfg.EntriesPerHolder = min(DefaultReplayEntriesPerHolder, cfg.MaxEntries)
	case cfg.EntriesPerHolder > cfg.MaxEntries:
		return nil, fmt.Errorf("dpop: replay cache share of one holder %d is larger than the cache, %d", cfg.EntriesPerHolder, cfg.MaxEntries)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &MemoryReplayCache{
		seen: map[replayKey]time.Time{}, perHolder: map[string]int{},
		maxEntries: cfg.MaxEntries, maxPerHolder: cfg.EntriesPerHolder, now: cfg.Now,
	}, nil
}

// Seen implements ReplayCache. Expired ids are dropped as they lapse, soonest
// first, so a full cache costs a lookup rather than a scan. A holder past its
// share, or a cache full of live ids, refuses the new id as a replay rather
// than accept it unrecorded — a full cache errs towards refusing.
func (c *MemoryReplayCache) Seen(_ context.Context, e ReplayEntry) (bool, error) {
	jkt, jti, holder, expires := e.JKT, e.JTI, e.Holder, e.ExpiresAt
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for len(c.expiries) > 0 && !c.expiries[0].expires.After(now) {
		lapsed := heap.Pop(&c.expiries).(replayEntry)
		delete(c.seen, lapsed.key)
		if c.perHolder[lapsed.holder]--; c.perHolder[lapsed.holder] <= 0 {
			delete(c.perHolder, lapsed.holder)
		}
	}
	k := replayKey{jkt: jkt, jti: jti}
	if _, ok := c.seen[k]; ok {
		return true, nil
	}
	if c.perHolder[holder] >= c.maxPerHolder || len(c.seen) >= c.maxEntries {
		return true, nil
	}
	c.seen[k] = expires
	c.perHolder[holder]++
	heap.Push(&c.expiries, replayEntry{key: k, holder: holder, expires: expires})
	return false, nil
}

// replayEntry is one id a MemoryReplayCache holds, whose share it counts
// against, and when it lapses.
type replayEntry struct {
	key     replayKey
	holder  string
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
	raw, err := jose.UnB64(jkt)
	return err == nil && len(raw) == sha256.Size
}
