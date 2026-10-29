package limes

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeForm is a TokenForm for the core's own tests, which may not resolve
// biscuit-go: a sealed JWT and its blocks as base64url JSON. It checks no
// signature but the root's: a token sealed under another root is refused,
// as a Biscuit's chain would be. What a real form proves of its encoding —
// that no block can be stripped, nor a token forged — is the biscuit
// module's to test.
type fakeForm struct{}

// fakeToken is a fakeForm token, before its encoding.
type fakeToken struct {
	Root   []byte
	Sealed string
	Blocks []Attenuation
	IDs    [][]byte
}

// fakeIDBytes is the length of a fakeForm revocation id.
const fakeIDBytes = 16

func fakeID() []byte {
	id := make([]byte, fakeIDBytes)
	_, _ = rand.Read(id)
	return id
}

func (fakeForm) Seal(ctx context.Context, sign func(context.Context, ed25519.PublicKey) (string, error)) (string, error) {
	root, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	sealed, err := sign(ctx, root)
	if err != nil {
		return "", err
	}
	return encodeFake(fakeToken{Root: root, Sealed: sealed, IDs: [][]byte{fakeID()}})
}

func (fakeForm) Open(token string) (string, func(ed25519.PublicKey) (Unsealed, error), error) {
	t, err := decodeFake(token)
	if err != nil {
		return "", nil, err
	}
	unseal := func(root ed25519.PublicKey) (Unsealed, error) {
		if !bytes.Equal(root, t.Root) {
			return Unsealed{}, errors.New("fake form: sealed under another root")
		}
		return Unsealed{Blocks: t.Blocks, RevocationIDs: t.IDs}, nil
	}
	return t.Sealed, unseal, nil
}

// attenuate is Attenuate for a fakeForm token: it appends a as a block,
// refusing what the biscuit module's Attenuate refuses.
func attenuate(token string, a Attenuation) (string, error) {
	t, err := decodeFake(token)
	if err != nil {
		return "", err
	}
	if len(t.Blocks) >= MaxBiscuitBlocks {
		return "", invalidRequest("the token already has %d attenuation blocks, MaxBiscuitBlocks", len(t.Blocks))
	}
	if err := a.Validate(); err != nil {
		return "", err
	}
	// A Biscuit date is whole seconds.
	if !a.ExpiresAt.IsZero() {
		a.ExpiresAt = a.ExpiresAt.UTC().Truncate(time.Second)
	}
	t.Blocks = append(t.Blocks, a)
	t.IDs = append(t.IDs, fakeID())
	return encodeFake(t)
}

func encodeFake(t fakeToken) (string, error) {
	raw, err := json.Marshal(t)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeFake(token string) (fakeToken, error) {
	var t fakeToken
	if strings.Contains(token, ".") {
		return t, fmt.Errorf("%w: not a Biscuit token", ErrInvalidToken)
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return t, fmt.Errorf("%w: fake form: %w", ErrInvalidToken, err)
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		return t, fmt.Errorf("%w: fake form: %w", ErrInvalidToken, err)
	}
	return t, nil
}

// fakeIDs are a fakeForm token's revocation ids, the sealing one first.
func fakeIDs(t *testing.T, token string) [][]byte {
	t.Helper()
	f, err := decodeFake(token)
	if err != nil {
		t.Fatal(err)
	}
	return f.IDs
}

// fakeAppend appends a to a fakeForm token as its holder could, unchecked:
// what the verifier must refuse whatever form carried it.
func fakeAppend(t *testing.T, token string, a Attenuation) string {
	t.Helper()
	f, err := decodeFake(token)
	if err != nil {
		t.Fatal(err)
	}
	f.Blocks = append(f.Blocks, a)
	f.IDs = append(f.IDs, fakeID())
	out, err := encodeFake(f)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
