// Package jose holds the JOSE pieces the module's packages share: the
// algorithm and key identifiers, base64url, the compact serialisation, and
// the public JWK with its RFC 7638 thumbprint. One definition each, so a
// token, a proof and a key set spell them alike.
package jose

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// JWS algorithms and JWK members (RFC 7518, RFC 8037, RFC 9864).
const (
	// AlgEd25519 is Ed25519 as RFC 9864 fully specifies it: what JWKS
	// entries and proofs this module writes carry, and tokens by default.
	AlgEd25519 = "Ed25519"
	// AlgEdDSA is the polymorphic identifier RFC 9864 deprecates. A token,
	// a DPoP proof or a JWKS entry naming it is read; a token names it when
	// its signer is configured to, for JOSE libraries that know no other.
	AlgEdDSA   = "EdDSA"
	AlgES256   = "ES256"
	KtyOKP     = "OKP"
	KtyEC      = "EC"
	CrvEd25519 = "Ed25519"
	CrvP256    = "P-256"
)

// P256CoordinateBytes is the size of a P-256 point coordinate, and of each
// half of an ES256 signature.
const P256CoordinateBytes = 32

// uncompressedPoint is the SEC 1 prefix of an uncompressed curve point.
const uncompressedPoint = 0x04

// MaxIJSONInteger is the largest integer I-JSON (RFC 7493 §2.2) carries:
// 2^53 − 1, the last a float64 holds exactly.
const MaxIJSONInteger = 1<<53 - 1

// B64 is b in base64url without padding, as JOSE writes every binary value.
func B64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// lineBreaks are the characters encoding/base64 skips while decoding, even
// in strict mode.
const lineBreaks = "\r\n"

// UnB64 decodes unpadded base64url strictly: the spare bits of the last
// character must be zero and no line break may appear, so each byte string
// has one spelling and a signature or token cannot be altered and still
// decode to the same bytes.
func UnB64(s string) ([]byte, error) {
	if strings.ContainsAny(s, lineBreaks) {
		return nil, errors.New("base64url: line break in encoded data")
	}
	return base64.RawURLEncoding.Strict().DecodeString(s)
}

// Parts is a compact JWS split into its three segments.
type Parts struct {
	Header, Payload, Signature string
}

// Split splits a compact JWS into its three segments.
func Split(token string) (Parts, error) {
	h, rest, ok1 := strings.Cut(token, ".")
	p, sig, ok2 := strings.Cut(rest, ".")
	if !ok1 || !ok2 || strings.Contains(sig, ".") {
		return Parts{}, errors.New("token must have 3 segments")
	}
	return Parts{Header: h, Payload: p, Signature: sig}, nil
}

// SigningInput is what the signature covers: header.payload.
func (p Parts) SigningInput() string { return p.Header + "." + p.Payload }

// JWK is a public key as RFC 7517 writes it: an Ed25519 (OKP) or P-256 (EC)
// key, its public members only.
type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y,omitempty"`
}

// PublicJWK describes pub, an ed25519.PublicKey or a P-256
// *ecdsa.PublicKey, as a JWK.
func PublicJWK(pub crypto.PublicKey) (JWK, error) {
	switch k := pub.(type) {
	case ed25519.PublicKey:
		return JWK{Kty: KtyOKP, Crv: CrvEd25519, X: B64(k)}, nil
	case *ecdsa.PublicKey:
		if k.Curve != elliptic.P256() {
			return JWK{}, errors.New("EC keys must be P-256")
		}
		raw, err := k.Bytes() // 0x04 || X || Y
		if err != nil {
			return JWK{}, fmt.Errorf("EC key: %w", err)
		}
		x, y := raw[1:1+P256CoordinateBytes], raw[1+P256CoordinateBytes:]
		return JWK{Kty: KtyEC, Crv: CrvP256, X: B64(x), Y: B64(y)}, nil
	}
	return JWK{}, fmt.Errorf("unsupported key type %T", pub)
}

// Thumbprint is the key's RFC 7638 SHA-256 thumbprint, base64url.
func (j JWK) Thumbprint() (string, error) {
	// RFC 7638 §3.2: the required members only, in lexicographic order, as
	// JSON with no whitespace. The structs' field order is that order.
	var canonical []byte
	var err error
	switch j.Kty {
	case KtyOKP:
		canonical, err = json.Marshal(struct {
			Crv string `json:"crv"`
			Kty string `json:"kty"`
			X   string `json:"x"`
		}{j.Crv, KtyOKP, j.X})
	case KtyEC:
		canonical, err = json.Marshal(struct {
			Crv string `json:"crv"`
			Kty string `json:"kty"`
			X   string `json:"x"`
			Y   string `json:"y"`
		}{j.Crv, KtyEC, j.X, j.Y})
	default:
		return "", fmt.Errorf("unsupported JWK kty %q", j.Kty)
	}
	if err != nil {
		return "", fmt.Errorf("JWK thumbprint: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return B64(sum[:]), nil
}

// PublicKey is the key j describes.
func (j JWK) PublicKey() (crypto.PublicKey, error) {
	x, err := UnB64(j.X)
	if err != nil {
		return nil, err
	}
	switch {
	case j.Kty == KtyOKP && j.Crv == CrvEd25519:
		if len(x) != ed25519.PublicKeySize {
			return nil, errors.New("Ed25519 key wrong length")
		}
		return ed25519.PublicKey(x), nil
	case j.Kty == KtyEC && j.Crv == CrvP256:
		y, err := UnB64(j.Y)
		if err != nil || len(x) != P256CoordinateBytes || len(y) != P256CoordinateBytes {
			return nil, errors.New("P-256 key malformed")
		}
		raw := append(append([]byte{uncompressedPoint}, x...), y...)
		return ecdsa.ParseUncompressedPublicKey(elliptic.P256(), raw)
	}
	return nil, fmt.Errorf("unsupported JWK %s/%s", j.Kty, j.Crv)
}

// Ed25519Thumbprint is the RFC 7638 thumbprint of an Ed25519 key, which
// cannot fail.
func Ed25519Thumbprint(pub ed25519.PublicKey) string {
	thumb, _ := JWK{Kty: KtyOKP, Crv: CrvEd25519, X: B64(pub)}.Thumbprint()
	return thumb
}

// VerifyEd25519 checks the Ed25519 signature over parts against pub.
func VerifyEd25519(parts Parts, pub ed25519.PublicKey) error {
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("public key wrong length %d", len(pub))
	}
	sig, err := UnB64(parts.Signature)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	if !ed25519.Verify(pub, []byte(parts.SigningInput()), sig) {
		return errors.New("signature does not verify")
	}
	return nil
}
