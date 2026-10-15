package limes

import (
	"crypto/ed25519"
	"fmt"
)

// redactedKey stands in for a private key wherever a value holding one is
// formatted: under %v, %+v, %#v, %s, %x or any other verb it prints
// redactedKeyText, never the key, so a log line or an error that formats a
// SigningKey, a SignerConfig or a Signer does not carry the key with it.
type redactedKey struct{}

// redactedKeyText is what a formatted private key reads as.
const redactedKeyText = "[redacted]"

func (redactedKey) String() string   { return redactedKeyText }
func (redactedKey) GoString() string { return redactedKeyText }

// signingKeyView is a SigningKey as it is formatted.
type signingKeyView struct {
	KeyID   string
	Public  ed25519.PublicKey
	Private redactedKey
}

// Format prints k with its private key redacted.
func (k SigningKey) Format(f fmt.State, verb rune) {
	_, _ = fmt.Fprintf(f, fmt.FormatString(f, verb), signingKeyView{KeyID: k.KeyID, Public: k.Public})
}

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

// signerView is a Signer as it is formatted.
type signerView struct {
	KeyID string
	Key   redactedKey
}

// Format prints s with its key redacted.
func (s Signer) Format(f fmt.State, verb rune) {
	_, _ = fmt.Fprintf(f, fmt.FormatString(f, verb), signerView{KeyID: s.keyID})
}
