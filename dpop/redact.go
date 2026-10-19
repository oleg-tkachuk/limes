package dpop

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// redactedKeys stands in for HMAC nonce keys wherever a value holding them is
// formatted, encoded as JSON or logged through slog: under any verb, and in
// JSON, it reads as how many keys there are, never their bytes, so a log line
// or an error that carries an HMACNoncesConfig or HMACNonces does not carry
// the secret that signs every nonce.
type redactedKeys int

// redactedKeysFormat is how a list of redacted keys reads.
const redactedKeysFormat = "[%d redacted]"

func (n redactedKeys) String() string   { return fmt.Sprintf(redactedKeysFormat, int(n)) }
func (n redactedKeys) GoString() string { return n.String() }

// MarshalText makes encoding/json and slog's handlers write the keys as
// String does.
func (n redactedKeys) MarshalText() ([]byte, error) { return []byte(n.String()), nil }

// sealedKeys holds HMACNonces' keys where no formatting reaches them.
// HMACNonces' own Format redacts them, but fmt does not call it on an
// HMACNonces it meets in an unexported field — a Verifier's, a consumer's —
// and under a verb a pointer does not take (%s, %q, %t) it prints that
// pointer's target field by field instead. Behind a func the keys are out of
// that walk: fmt prints a func as its address under every verb.
type sealedKeys struct{ open func() [][]byte }

func sealKeys(keys [][]byte) *sealedKeys {
	return &sealedKeys{open: func() [][]byte { return keys }}
}

// get returns the keys; none for a zero HMACNonces, which holds no seal.
func (k *sealedKeys) get() [][]byte {
	if k == nil {
		return nil
	}
	return k.open()
}

// hmacNoncesView is an HMACNoncesConfig or HMACNonces as it is formatted.
type hmacNoncesView struct {
	Keys     redactedKeys
	Lifetime time.Duration
}

// Format prints cfg with its keys redacted.
func (cfg HMACNoncesConfig) Format(f fmt.State, verb rune) {
	_, _ = fmt.Fprintf(f, fmt.FormatString(f, verb), cfg.view())
}

// MarshalJSON encodes cfg with its keys redacted.
func (cfg HMACNoncesConfig) MarshalJSON() ([]byte, error) { return json.Marshal(cfg.view()) }

// LogValue logs cfg with its keys redacted, under any slog handler.
func (cfg HMACNoncesConfig) LogValue() slog.Value { return slog.AnyValue(cfg.view()) }

func (cfg HMACNoncesConfig) view() hmacNoncesView {
	return hmacNoncesView{Keys: redactedKeys(len(cfg.Keys)), Lifetime: cfg.Lifetime}
}

// Format prints n with its keys redacted. On the value, as Signer's: a
// copy of an HMACNonces formats as the pointer does.
func (n HMACNonces) Format(f fmt.State, verb rune) {
	_, _ = fmt.Fprintf(f, fmt.FormatString(f, verb), n.view())
}

// MarshalJSON encodes n with its keys redacted.
func (n HMACNonces) MarshalJSON() ([]byte, error) { return json.Marshal(n.view()) }

// LogValue logs n with its keys redacted, under any slog handler.
func (n HMACNonces) LogValue() slog.Value { return slog.AnyValue(n.view()) }

func (n HMACNonces) view() hmacNoncesView {
	return hmacNoncesView{Keys: redactedKeys(len(n.keys.get())), Lifetime: n.lifetime}
}
