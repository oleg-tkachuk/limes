package limes

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// JWKSDocument is the on-the-wire shape served at the issuer's JWKS
// endpoint (JWKSHandler; typically `/.well-known/jwks.json`). Verifiers in
// other services fetch and cache this document (RemoteJWKSResolver) so
// capability verification stays local without an extra call per request.
//
// Format: RFC 7517 with kty=OKP and crv=Ed25519 (RFC 8037), alg=Ed25519
// (RFC 9864).
//
// Serve capability keys from their own document, separate from any IdP
// JWKS: capability tokens use a distinct algorithm, issuer set and
// rotation cadence, and one shared document would conflate two trust
// roots.
type JWKSDocument struct {
	Keys []JWK `json:"keys"`
}

// JWK is one entry in the JWKS — exactly the fields needed to verify
// an Ed25519 signature; we do not embed `use` or `key_ops` because
// the kid + alg pair fully constrains use here.
type JWK struct {
	Kty string `json:"kty"`           // "OKP"
	Crv string `json:"crv"`           // "Ed25519"
	Kid string `json:"kid"`           // matches jwtHeader.Kid
	Alg string `json:"alg,omitempty"` // "Ed25519" (RFC 9864); "EdDSA" is read
	X   string `json:"x"`             // base64url-encoded raw public key
}

// MarshalJWKS produces a JWKS document for the supplied kid → public key
// map, with entries sorted by kid so the same key set always serialises to
// the same bytes (cacheable, diffable, ETag-able).
func MarshalJWKS(keys map[string]ed25519.PublicKey) ([]byte, error) {
	doc := JWKSDocument{Keys: make([]JWK, 0, len(keys))}
	for _, kid := range slices.Sorted(maps.Keys(keys)) {
		pub := keys[kid]
		if len(pub) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("limes: public key %q wrong size for Ed25519", kid)
		}
		doc.Keys = append(doc.Keys, JWK{
			Kty: jwkKtyOKP,
			Crv: jwkCrvEd25519,
			Alg: algEd25519,
			Kid: kid,
			X:   base64.RawURLEncoding.EncodeToString(pub),
		})
	}
	return json.Marshal(doc)
}

// ParseJWKS decodes a JWKS document into a kid → public key map. Entries
// that are not Ed25519 (a future kty), that lack a kid, or whose key does
// not decode to an Ed25519 public key are skipped rather than failing the
// document: one malformed or foreign entry must not take every other key —
// and so every token signed with them — out of service during a rotation.
// A document that is not JSON at all is an error.
func ParseJWKS(raw []byte) (map[string]ed25519.PublicKey, error) {
	keys, _, err := parseJWKS(raw)
	return keys, err
}

// parseJWKS is ParseJWKS, also reporting how many entries it skipped.
func parseJWKS(raw []byte) (map[string]ed25519.PublicKey, int, error) {
	var doc JWKSDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, 0, fmt.Errorf("limes: parse JWKS: %w", err)
	}
	out := make(map[string]ed25519.PublicKey, len(doc.Keys))
	skipped := 0
	for _, k := range doc.Keys {
		if k.Kty != jwkKtyOKP || k.Crv != jwkCrvEd25519 || k.Kid == "" {
			skipped++
			continue
		}
		if k.Alg != "" && k.Alg != algEd25519 && k.Alg != algEdDSA {
			skipped++
			continue
		}
		decoded, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			skipped++
			continue
		}
		out[k.Kid] = ed25519.PublicKey(decoded)
	}
	return out, skipped, nil
}

// JWKSHandler serves keys() as a JWK Set document (RFC 7517), the way the
// RemoteJWKSResolver on the other end reads it: under its registered media
// type (§8.5), with a strong ETag of its bytes that answers If-None-Match
// with 304 (RFC 9110 §13.1.2), and fresh for maxAge (RFC 9111 §5.2.2.1).
// Keep maxAge below the time between publishing a new key and signing with
// it, and between withdrawing an old key and the end of its tokens' TTL; a
// negative maxAge is served as zero.
func JWKSHandler(keys func() map[string]ed25519.PublicKey, maxAge time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		doc, err := MarshalJWKS(keys())
		if err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		sum := sha256.Sum256(doc)
		etag := `"` + base64.RawURLEncoding.EncodeToString(sum[:]) + `"`
		h := w.Header()
		h.Set("ETag", etag)
		h.Set(headerCacheControl, fmt.Sprintf("public, %s=%d", directiveMaxAge, int64(max(maxAge, 0)/time.Second)))
		if etagMatches(r.Header.Values("If-None-Match"), etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		h.Set("Content-Type", mediaTypeJWKSet)
		h.Set("Content-Length", strconv.Itoa(len(doc)))
		_, _ = w.Write(doc)
	})
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
