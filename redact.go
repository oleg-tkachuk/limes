package limes

import (
	"crypto"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
)

// redactedKey stands in for a private key wherever a value holding one is
// formatted, encoded as JSON or logged through slog: under %v, %+v, %#v, %s,
// %x or any other verb, and in JSON, it reads redactedKeyText, never the key,
// so a log line or an error that carries a SigningKey, a SignerConfig or a
// Signer does not carry the key with it.
type redactedKey struct{}

// redactedKeyText is what a formatted private key reads as.
const redactedKeyText = "[redacted]"

func (redactedKey) String() string   { return redactedKeyText }
func (redactedKey) GoString() string { return redactedKeyText }

// MarshalText makes encoding/json and slog's handlers write the key as
// redactedKeyText too.
func (redactedKey) MarshalText() ([]byte, error) { return []byte(redactedKeyText), nil }

// sealedSigner holds a Signer's key where no formatting reaches it. Signer's
// own Format redacts the key, but fmt does not call it on a Signer it meets
// in an unexported field — an Issuer's, a consumer's — and under a verb a
// pointer does not take (%s, %q, %t) it prints that pointer's target
// field by field instead. Behind a func the key is out of that walk: fmt
// prints a func as its address under every verb.
type sealedSigner struct{ open func() crypto.Signer }

func sealSigner(key crypto.Signer) *sealedSigner {
	return &sealedSigner{open: func() crypto.Signer { return key }}
}

// SealedKey is a private key where no formatting reaches it: under any
// verb, %p included, in JSON and in slog, and wherever it sits — a field of
// a struct of yours, exported or not — it reads redactedKeyText. A plain
// key field cannot promise that: fmt walks into a field it cannot call a
// method on, and under a verb a value does not take. SealKey makes one; the
// zero SealedKey holds no key. It is a crypto.Signer over the key it holds.
type SealedKey struct{ s *sealedSigner }

// SealKey seals key: an ed25519.PrivateKey, or a crypto.Signer over a key
// held elsewhere. A nil key gives the zero SealedKey.
func SealKey(key crypto.Signer) SealedKey {
	if key == nil {
		return SealedKey{}
	}
	return SealedKey{sealSigner(key)}
}

// signer is the key sealed; nil for the zero SealedKey.
func (k SealedKey) signer() crypto.Signer {
	if k.s == nil {
		return nil
	}
	return k.s.open()
}

// usable is the key sealed, nil when there is none or it is an
// ed25519.PrivateKey of the wrong length, whose Public and Sign panic.
func (k SealedKey) usable() crypto.Signer {
	s := k.signer()
	if p, ok := s.(ed25519.PrivateKey); ok && len(p) != ed25519.PrivateKeySize {
		return nil
	}
	return s
}

// IsZero reports whether k holds no key.
func (k SealedKey) IsZero() bool { return k.s == nil }

// Public is the key's public half; nil when k holds no usable key.
func (k SealedKey) Public() crypto.PublicKey {
	s := k.usable()
	if s == nil {
		return nil
	}
	return s.Public()
}

// Sign signs digest with the key, as crypto.Signer; an error when k holds
// no usable key.
func (k SealedKey) Sign(rand io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	s := k.usable()
	if s == nil {
		return nil, errors.New("limes: a SealedKey holding no usable key")
	}
	return s.Sign(rand, digest, opts)
}

func (SealedKey) String() string   { return redactedKeyText }
func (SealedKey) GoString() string { return redactedKeyText }

// Format prints the key as redactedKeyText under every verb.
func (SealedKey) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, redactedKeyText) }

// MarshalText makes encoding/json and slog's handlers write the key as
// redactedKeyText too.
func (SealedKey) MarshalText() ([]byte, error) { return []byte(redactedKeyText), nil }

// signingKeyView is a SigningKey as it is formatted.
type signingKeyView struct {
	KeyID   string
	Public  ed25519.PublicKey
	Private redactedKey
}

func (k SigningKey) view() signingKeyView { return signingKeyView{KeyID: k.KeyID, Public: k.Public()} }

// Format prints k with its private key redacted.
func (k SigningKey) Format(f fmt.State, verb rune) {
	_, _ = fmt.Fprintf(f, fmt.FormatString(f, verb), k.view())
}

// MarshalJSON encodes k with its private key redacted: a SigningKey written
// as JSON, to a log or anywhere else, is not a way to carry the key.
func (k SigningKey) MarshalJSON() ([]byte, error) { return json.Marshal(k.view()) }

// LogValue logs k with its private key redacted, under any slog handler.
func (k SigningKey) LogValue() slog.Value { return slog.AnyValue(k.view()) }

// signerConfigView is a SignerConfig as it is formatted.
type signerConfigView struct {
	KeyID string
	Key   redactedKey
}

// Format prints cfg with its key redacted, whether the key is an
// ed25519.PrivateKey or a crypto.Signer that holds one.
func (cfg SignerConfig) Format(f fmt.State, verb rune) {
	_, _ = fmt.Fprintf(f, fmt.FormatString(f, verb), signerConfigView{KeyID: cfg.KeyID})
}

// MarshalJSON encodes cfg with its key redacted.
func (cfg SignerConfig) MarshalJSON() ([]byte, error) {
	return json.Marshal(signerConfigView{KeyID: cfg.KeyID})
}

// LogValue logs cfg with its key redacted, under any slog handler.
func (cfg SignerConfig) LogValue() slog.Value {
	return slog.AnyValue(signerConfigView{KeyID: cfg.KeyID})
}

// signerView is a Signer as it is formatted.
type signerView struct {
	KeyID string
	Key   redactedKey
}

// Format prints s with its key redacted.
func (s Signer) Format(f fmt.State, verb rune) {
	_, _ = fmt.Fprintf(f, fmt.FormatString(f, verb), signerView{KeyID: s.keyID})
}
