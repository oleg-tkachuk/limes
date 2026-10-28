// Package jwks publishes and resolves the Ed25519 keys capability tokens are
// signed with, as an RFC 7517 JSON Web Key Set. NewHandler serves an issuer's
// keys; a verifier resolves a token's kid through a limes.KeyResolver — Static for
// keys it holds, Remote for an issuer's set fetched over HTTPS and cached.
// jwkstest checks a resolver of one's own against the same contract.
package jwks

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/oleg-tkachuk/limes/internal/httpx"
	"github.com/oleg-tkachuk/limes/internal/jose"
)

// document is the on-the-wire shape served at the issuer's JWKS
// endpoint (NewHandler; typically `/.well-known/jwks.json`). Verifiers in
// other services fetch and cache this document (Remote) so
// capability verification stays local without an extra call per request.
//
// Format: RFC 7517 with kty=OKP and crv=Ed25519 (RFC 8037), alg=Ed25519
// (RFC 9864).
//
// Serve capability keys from their own document, separate from any IdP
// JWKS: capability tokens use a distinct algorithm, issuer set and
// rotation cadence, and one shared document would conflate two trust
// roots.
type document struct {
	// Keys is nil when the document has no "keys" member, or a null one:
	// RFC 7517 §5 requires it, and only an empty array withdraws every key.
	Keys *[]entry `json:"keys"`
}

// received is a document as parse reads it: each entry kept as the JSON it
// is, so one of a shape the reader does not support is skipped on its own
// rather than failing the set (RFC 7517 §5).
type received struct {
	// Keys is nil as document's is.
	Keys *[]jsontext.Value `json:"keys"`
}

// entry is one entry in the JWKS — exactly the fields needed to verify
// an Ed25519 signature. Marshal writes no `use` or `key_ops`, as the kid +
// alg pair fully constrains use here; parse reads them, so a key another
// issuer publishes for something else is not used to verify (RFC 7517 §4.2,
// §4.3).
type entry struct {
	Kty    string   `json:"kty"`               // "OKP"
	Crv    string   `json:"crv"`               // "Ed25519"
	Kid    string   `json:"kid"`               // matches jwtHeader.Kid
	Alg    string   `json:"alg,omitempty"`     // "Ed25519" (RFC 9864); "EdDSA" is read
	Use    string   `json:"use,omitempty"`     // "sig" or none to verify with
	KeyOps []string `json:"key_ops,omitempty"` // "verify" among them, or none
	X      string   `json:"x"`                 // base64url-encoded raw public key
}

// What an entry's use and key_ops name when its key verifies signatures
// (RFC 7517 §4.2, §4.3).
const (
	useSignature = "sig"
	opVerify     = "verify"
)

// verifies reports whether k's use and key_ops let it verify a signature:
// each unset, or naming that use.
func (k entry) verifies() bool {
	if k.Use != "" && k.Use != useSignature {
		return false
	}
	// An empty key_ops is set, and names no operation.
	return k.KeyOps == nil || slices.Contains(k.KeyOps, opVerify)
}

// The names a key's entry may give its algorithm (RFC 9864): the alg the
// tokens it signs name, as limes.SignerConfig.Algorithm writes it. A JOSE
// library that matches an entry's alg to a token's, Node's jose among them,
// finds a key only under the name its tokens use.
const (
	AlgorithmEd25519 = jose.AlgEd25519
	AlgorithmEdDSA   = jose.AlgEdDSA
)

// algorithms are the names an entry may give.
var algorithms = []string{AlgorithmEd25519, AlgorithmEdDSA}

// Marshal produces a JWKS document for the supplied kid → public key
// map, with entries sorted by kid so the same key set always serialises to
// the same bytes (cacheable, diffable, ETag-able). Every entry names
// AlgorithmEd25519; MarshalAlgorithms names another.
func Marshal(keys map[string]ed25519.PublicKey) ([]byte, error) {
	return MarshalAlgorithms(keys, nil)
}

// MarshalAlgorithms is Marshal with each key's alg named by algs, kid →
// name: AlgorithmEdDSA for a key whose signer writes EdDSA, so a library
// that matches the names finds it. A key algs does not name is
// AlgorithmEd25519's. A name for a kid keys does not hold, or a name that
// is neither, is refused: it would describe no key, or one no token names.
func MarshalAlgorithms(keys map[string]ed25519.PublicKey, algs map[string]string) ([]byte, error) {
	for kid, alg := range algs {
		if _, ok := keys[kid]; !ok {
			return nil, fmt.Errorf("jwks: an algorithm for %q, which is no key of the set", kid)
		}
		if !slices.Contains(algorithms, alg) {
			return nil, fmt.Errorf("jwks: algorithm %q for %q is not one of %q", alg, kid, algorithms)
		}
	}
	entries := make([]entry, 0, len(keys))
	for _, kid := range slices.Sorted(maps.Keys(keys)) {
		pub := keys[kid]
		if len(pub) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("jwks: public key %q wrong size for Ed25519", kid)
		}
		alg, ok := algs[kid]
		if !ok {
			alg = AlgorithmEd25519
		}
		entries = append(entries, entry{
			Kty: jose.KtyOKP,
			Crv: jose.CrvEd25519,
			Alg: alg,
			Kid: kid,
			X:   jose.B64(pub),
		})
	}
	return json.Marshal(document{Keys: &entries})
}

// Parse decodes a JWKS document into a kid → public key map. Entries
// that are not Ed25519 (a future kty), that lack a kid, whose use or
// key_ops is not verifying signatures (RFC 7517 §4.2, §4.3), whose key does
// not decode to an Ed25519 public key, or that are not of the shape a JWK
// is read in — a member of another JSON type, no object at all — are skipped
// rather than failing the document (RFC 7517 §5): one malformed or foreign
// entry must not take every other key — and so every token signed with them
// — out of service during a rotation. Two Ed25519 keys naming one kid are
// both skipped; a kid an entry of another kind also names is not (§4.5).
// A document that is not JSON at all, names a member twice, or has no
// "keys" array (RFC 7517 §5) is an error: only `{"keys":[]}` withdraws
// every key.
func Parse(raw []byte) (map[string]ed25519.PublicKey, error) {
	keys, _, err := parse(raw)
	return keys, err
}

// parse is Parse, also reporting how many entries it skipped.
func parse(raw []byte) (map[string]ed25519.PublicKey, int, error) {
	var doc received
	// JSON's own rules: names in their exact case, none twice in one object.
	if err := jsonv2.Unmarshal(raw, &doc); err != nil {
		return nil, 0, fmt.Errorf("jwks: parse JWKS: %w", err)
	}
	if doc.Keys == nil {
		return nil, 0, errors.New(`jwks: parse JWKS: no "keys" array`)
	}
	entries := *doc.Keys
	keys := make([]usable, 0, len(entries))
	named := make(map[string]int, len(entries)) // usable keys per kid
	skipped := 0
	for _, value := range entries {
		k, ok := usableKey(value)
		if !ok {
			skipped++
			continue
		}
		keys = append(keys, k)
		named[k.kid]++
	}
	out := make(map[string]ed25519.PublicKey, len(keys))
	for _, k := range keys {
		// A kid named by two usable keys is used for neither: which one
		// signs is not for the reader to guess. An entry of another kind
		// naming it too does not count (RFC 7517 §4.5): no token verifies
		// under that one.
		if named[k.kid] > 1 {
			skipped++
			continue
		}
		out[k.kid] = k.key
	}
	return out, skipped, nil
}

// usable is an entry's Ed25519 key under its kid.
type usable struct {
	kid string
	key ed25519.PublicKey
}

// usableKey is value's Ed25519 key, when value is an entry a verifier can
// use: an object whose members are each of their JSON type, for an Ed25519
// key under a kid, for an algorithm that is Ed25519's, encoded as JOSE
// encodes every member, once.
func usableKey(value jsontext.Value) (usable, bool) {
	var k entry
	if err := jsonv2.Unmarshal(value, &k); err != nil {
		return usable{}, false
	}
	if k.Kty != jose.KtyOKP || k.Crv != jose.CrvEd25519 || k.Kid == "" || !ed25519Alg(k.Alg) || !k.verifies() {
		return usable{}, false
	}
	decoded, err := jose.UnB64(k.X) // strict: one encoding per key, as every JOSE member
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		return usable{}, false
	}
	return usable{kid: k.Kid, key: ed25519.PublicKey(decoded)}, true
}

// ed25519Alg reports whether alg names Ed25519, or nothing.
func ed25519Alg(alg string) bool {
	return alg == "" || alg == jose.AlgEd25519 || alg == jose.AlgEdDSA
}

// HandlerConfig configures the handler that serves a key set.
type HandlerConfig struct {
	// Keys returns the keys to serve, kid → key. Required.
	Keys func() map[string]ed25519.PublicKey
	// Algorithms returns the alg each key's entry names, kid → name, as
	// MarshalAlgorithms takes them: AlgorithmEdDSA for a key whose signer
	// writes EdDSA. Nil, or a key it does not name: AlgorithmEd25519. A name
	// for a kid Keys no longer returns is ignored, and logged, so
	// withdrawing a key never waits on its name being removed.
	Algorithms func() map[string]string
	// Logger logs a name Algorithms gives for a kid Keys does not return,
	// at WARN, and a set the handler cannot serve, at ERROR. Nil:
	// slog.Default().
	Logger *slog.Logger
	// MaxAge is how long a client may keep the set (RFC 9111 §5.2.2.1).
	// Keep it below the time between publishing a new key and signing with
	// it, and between withdrawing an old key and the end of its tokens'
	// TTL. Served in whole seconds, the fraction dropped, so under a second
	// it is refused: it would read as Revalidate's max-age=0. Zero:
	// DefaultMaxAge.
	MaxAge time.Duration
	// Revalidate tells clients to revalidate the set on every use
	// (max-age=0) instead of keeping it for MaxAge, which it excludes.
	Revalidate bool
}

// DefaultMaxAge is how long a client may keep the set when the handler names
// no MaxAge: Remote's own DefaultRefreshInterval.
const DefaultMaxAge = DefaultRefreshInterval

// Handler serves a key set as a JWK Set document (RFC 7517), the way the
// Remote on the other end reads it: under its registered media type (§8.5),
// with a strong ETag of its bytes that answers If-None-Match with 304
// (RFC 9110 §13.1.2), and fresh for MaxAge.
type Handler struct {
	keys   func() map[string]ed25519.PublicKey
	algs   func() map[string]string
	maxAge time.Duration
	log    *slog.Logger
}

// NewHandler builds a Handler serving cfg.Keys(), refusing no Keys, a
// negative MaxAge or one under a second, or a MaxAge beside Revalidate.
func NewHandler(cfg HandlerConfig) (*Handler, error) {
	if cfg.Keys == nil {
		return nil, errors.New("jwks: HandlerConfig.Keys required")
	}
	maxAge, err := cfg.maxAge()
	if err != nil {
		return nil, err
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Handler{keys: cfg.Keys, algs: cfg.Algorithms, maxAge: maxAge, log: cfg.Logger}, nil
}

// maxAge is the max-age the handler serves: 0 for Revalidate, MaxAge, or
// DefaultMaxAge for none. It refuses a negative MaxAge or one under a
// second, or a MaxAge beside Revalidate.
func (cfg HandlerConfig) maxAge() (time.Duration, error) {
	switch {
	case cfg.MaxAge < 0:
		return 0, fmt.Errorf("jwks: HandlerConfig.MaxAge %s is negative", cfg.MaxAge)
	case cfg.MaxAge > 0 && cfg.MaxAge < time.Second:
		return 0, fmt.Errorf("jwks: HandlerConfig.MaxAge %s is under a second, the unit of max-age", cfg.MaxAge)
	case cfg.Revalidate && cfg.MaxAge != 0:
		return 0, fmt.Errorf("jwks: HandlerConfig.MaxAge %s beside Revalidate", cfg.MaxAge)
	case cfg.Revalidate:
		return 0, nil
	case cfg.MaxAge == 0:
		return DefaultMaxAge, nil
	}
	return cfg.MaxAge, nil
}

// ServeHTTP answers GET and HEAD with the document, and anything else with
// 405.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set(httpx.HeaderAllow, http.MethodGet+", "+http.MethodHead)
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	keys := h.keys()
	doc, err := MarshalAlgorithms(keys, h.servedAlgorithms(r.Context(), keys))
	if err != nil {
		h.log.ErrorContext(r.Context(), "jwks: the key set cannot be served", slog.Any("error", err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	sum := sha256.Sum256(doc)
	etag := `"` + jose.B64(sum[:]) + `"`
	header := w.Header()
	header.Set(httpx.HeaderETag, etag)
	header.Set(httpx.HeaderCacheControl, fmt.Sprintf("%s, %s=%d", httpx.DirectivePublic, httpx.DirectiveMaxAge, int64(h.maxAge/time.Second)))
	if etagMatches(r.Header.Values(httpx.HeaderIfNoneMatch), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	header.Set(httpx.HeaderContentType, mediaTypeJWKSet)
	header.Set(httpx.HeaderContentLength, strconv.Itoa(len(doc)))
	_, _ = w.Write(doc)
}

// servedAlgorithms is the names Algorithms gives for the kids in keys. A
// name for a key no longer served is dropped, not refused: a withdrawn key
// whose name was left behind would otherwise fail the whole document, and
// every client would keep the set it has, withdrawn key included, until it
// gives up on the set.
func (h *Handler) servedAlgorithms(ctx context.Context, keys map[string]ed25519.PublicKey) map[string]string {
	if h.algs == nil {
		return nil
	}
	algs := maps.Clone(h.algs())
	for kid := range algs {
		if _, ok := keys[kid]; !ok {
			delete(algs, kid)
			h.log.WarnContext(ctx, "jwks: an algorithm named for a key not served is dropped", slog.String("kid", kid))
		}
	}
	return algs
}

// etagMatches is If-None-Match's weak comparison (RFC 9110 §13.1.2): any
// listed tag, W/ or not, equal to etag, or "*".
func etagMatches(ifNoneMatch []string, etag string) bool {
	for _, line := range ifNoneMatch {
		for tag := range strings.SplitSeq(line, ",") {
			tag = strings.TrimPrefix(strings.TrimSpace(tag), "W/")
			if tag == "*" || tag == etag {
				return true
			}
		}
	}
	return false
}
