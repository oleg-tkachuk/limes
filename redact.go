package limes

import (
	"crypto"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
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
