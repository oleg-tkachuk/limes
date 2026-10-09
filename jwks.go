package limes

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
)

// JWKSDocument is the on-the-wire shape served at the issuer's JWKS
// endpoint (typically `/.well-known/jwks.json` on the admin plane).
// Verifiers in other planes / pods fetch and cache this document with
// a short TTL so capability verification stays local without an extra
// RPC per request.
//
// Format: RFC 7517 with kty=OKP and crv=Ed25519 (RFC 8037).
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
	Alg string `json:"alg,omitempty"` // "EdDSA"
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
			Kty: "OKP",
			Crv: "Ed25519",
			Alg: "EdDSA",
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
	var doc JWKSDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("limes: parse JWKS: %w", err)
	}
	out := make(map[string]ed25519.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kty != "OKP" || k.Crv != "Ed25519" || k.Kid == "" {
			continue
		}
		if k.Alg != "" && k.Alg != "EdDSA" {
			continue
		}
		decoded, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			continue
		}
		out[k.Kid] = ed25519.PublicKey(decoded)
	}
	return out, nil
}
