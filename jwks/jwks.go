// Package jwks publishes and resolves the Ed25519 keys capability tokens are
// signed with, as an RFC 7517 JSON Web Key Set. NewHandler serves an issuer's
// keys; a verifier resolves a token's kid through a limes.KeyResolver — Static for
// keys it holds, Remote for an issuer's set fetched over HTTP and cached.
// jwkstest checks a resolver of one's own against the same contract.
package jwks

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	jsonv2 "github.com/go-json-experiment/json"

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
	Keys []entry `json:"keys"`
}

// entry is one entry in the JWKS — exactly the fields needed to verify
// an Ed25519 signature; we do not embed `use` or `key_ops` because
// the kid + alg pair fully constrains use here.
type entry struct {
	Kty string `json:"kty"`           // "OKP"
	Crv string `json:"crv"`           // "Ed25519"
	Kid string `json:"kid"`           // matches jwtHeader.Kid
	Alg string `json:"alg,omitempty"` // "Ed25519" (RFC 9864); "EdDSA" is read
	X   string `json:"x"`             // base64url-encoded raw public key
}

// Marshal produces a JWKS document for the supplied kid → public key
// map, with entries sorted by kid so the same key set always serialises to
// the same bytes (cacheable, diffable, ETag-able).
func Marshal(keys map[string]ed25519.PublicKey) ([]byte, error) {
	doc := document{Keys: make([]entry, 0, len(keys))}
	for _, kid := range slices.Sorted(maps.Keys(keys)) {
		pub := keys[kid]
		if len(pub) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("jwks: public key %q wrong size for Ed25519", kid)
		}
		doc.Keys = append(doc.Keys, entry{
			Kty: jose.KtyOKP,
			Crv: jose.CrvEd25519,
			Alg: jose.AlgEd25519,
			Kid: kid,
			X:   jose.B64(pub),
		})
	}
	return json.Marshal(doc)
}

// Parse decodes a JWKS document into a kid → public key map. Entries
// that are not Ed25519 (a future kty), that lack a kid, or whose key does
// not decode to an Ed25519 public key are skipped rather than failing the
// document: one malformed or foreign entry must not take every other key —
// and so every token signed with them — out of service during a rotation.
// Two entries naming one kid are both skipped. A document that is not JSON
// at all, or names a member twice, is an error.
func Parse(raw []byte) (map[string]ed25519.PublicKey, error) {
	keys, _, err := parse(raw)
	return keys, err
}

// parse is Parse, also reporting how many entries it skipped.
func parse(raw []byte) (map[string]ed25519.PublicKey, int, error) {
	var doc document
	// JSON's own rules: names in their exact case, none twice in one object.
	if err := jsonv2.Unmarshal(raw, &doc); err != nil {
		return nil, 0, fmt.Errorf("jwks: parse JWKS: %w", err)
	}
	out := make(map[string]ed25519.PublicKey, len(doc.Keys))
	seen := make(map[string]int, len(doc.Keys)) // entries per kid
	for _, k := range doc.Keys {
		seen[k.Kid]++
	}
	skipped := 0
	for _, k := range doc.Keys {
		// A kid named twice is used for neither key: which one signs is not
		// for the reader to guess.
		if k.Kty != jose.KtyOKP || k.Crv != jose.CrvEd25519 || k.Kid == "" || seen[k.Kid] > 1 {
			skipped++
			continue
		}
		if k.Alg != "" && k.Alg != jose.AlgEd25519 && k.Alg != jose.AlgEdDSA {
			skipped++
			continue
		}
		decoded, err := jose.UnB64(k.X) // strict: one encoding per key, as every JOSE member
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			skipped++
			continue
		}
		out[k.Kid] = ed25519.PublicKey(decoded)
	}
	return out, skipped, nil
}

// HandlerConfig configures the handler that serves a key set.
type HandlerConfig struct {
	// Keys returns the keys to serve, kid → key. Required.
	Keys func() map[string]ed25519.PublicKey
	// MaxAge is how long a client may keep the set (RFC 9111 §5.2.2.1).
	// Keep it below the time between publishing a new key and signing with
	// it, and between withdrawing an old key and the end of its tokens'
	// TTL. Zero: DefaultMaxAge.
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
	maxAge time.Duration
}

// NewHandler builds a Handler serving cfg.Keys(), refusing no Keys, a
// negative MaxAge, or a MaxAge beside Revalidate.
func NewHandler(cfg HandlerConfig) (*Handler, error) {
	if cfg.Keys == nil {
		return nil, errors.New("jwks: HandlerConfig.Keys required")
	}
	switch {
	case cfg.MaxAge < 0:
		return nil, fmt.Errorf("jwks: HandlerConfig.MaxAge %s is negative", cfg.MaxAge)
	case cfg.Revalidate && cfg.MaxAge != 0:
		return nil, fmt.Errorf("jwks: HandlerConfig.MaxAge %s beside Revalidate", cfg.MaxAge)
	case cfg.Revalidate:
		// maxAge stays 0: max-age=0.
	case cfg.MaxAge == 0:
		cfg.MaxAge = DefaultMaxAge
	}
	return &Handler{keys: cfg.Keys, maxAge: cfg.MaxAge}, nil
}

// ServeHTTP answers GET and HEAD with the document, and anything else with
// 405.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set(httpx.HeaderAllow, http.MethodGet+", "+http.MethodHead)
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	doc, err := Marshal(h.keys())
	if err != nil {
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
