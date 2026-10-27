package limes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
	"uuid"
)

// Mints the golden-token fixtures golden_test.go verifies, one per token
// type. Run explicitly, and only for a deliberate wire-format change, naming
// the type whose fixture to write; another type's stays as it was minted:
//
//	LIMES_WRITE_GOLDEN=at+jwt go test . -run TestGenerateGoldenToken
//
// Every input below is fixed so golden_test.go can reconstruct the same key
// and clock.

// goldenSeed is the fixed Ed25519 seed the fixtures are minted from.
var goldenSeed = [32]byte{
	0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
	0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10,
	0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18,
	0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20,
}

const (
	goldenKID      = "golden-kid"
	goldenIssuer   = "limes-golden"
	goldenAudience = "limes-data"
	goldenSubject  = "golden-agent"
	goldenTenant   = "11111111-1111-1111-1111-111111111111"
	// goldenClientID is the client a v7 fixture names; a v6 one carries none.
	goldenClientID = "golden-client"
	// goldenUser is the user a v7 fixture's agent acts for; a v6 one names
	// none.
	goldenUser = "golden-user"

	// goldenWriteEnv names the token type whose fixture is written anew.
	goldenWriteEnv = "LIMES_WRITE_GOLDEN"
)

// goldenFiles are the fixtures of the token types this package reads, one
// each: what TestGenerateGoldenToken writes.
var goldenFiles = map[TokenType]string{
	TokenTypeV6: "golden_token_v6.jwt",
	TokenTypeV7: "golden_token_v7.jwt",
}

// retiredFixtures are tokens of the retired formats, each from before its
// type was retired, kept to show they are refused rather than read under
// another schema.
var retiredFixtures = map[string]string{
	"v4": "golden_token_v4.jwt",
	"v5": "golden_token_v5.jwt",
}

// goldenCopyQuota is the golden capability's quota of Biscuit copies, so
// that the fixture carries max_copies.
const goldenCopyQuota = 8

// goldenPath is the fixture of token type typ.
func goldenPath(typ TokenType) string { return filepath.Join("testdata", goldenFiles[typ]) }

// goldenClock is the fixed issuance instant. The module-side verifier pins
// the same value so nbf/exp stay inside the window forever.
func goldenClock() time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
}

// genStore is a no-op Store — issuance only needs Insert to succeed.
type genStore struct{}

func (genStore) Insert(context.Context, InsertRequest) error { return nil }
func (genStore) GetRecord(context.Context, uuid.UUID) (Record, error) {
	return Record{}, errors.New("not found")
}
func (genStore) IsRevoked(context.Context, uuid.UUID) (bool, error) { return false, nil }
func (genStore) Revoke(context.Context, RevokeRequest) (RevokeResult, error) {
	return RevokeResult{}, nil
}
func (genStore) Purge(context.Context, PurgeRequest) (PurgeResult, error) {
	return PurgeResult{}, nil
}
func (genStore) ListByPrincipal(context.Context, ListByPrincipalRequest) (Page[Capability], error) {
	return Page[Capability]{}, nil
}

func TestGenerateGoldenToken(t *testing.T) {
	typ := TokenType(os.Getenv(goldenWriteEnv))
	if _, ok := goldenFiles[typ]; !ok {
		t.Skipf("set %s to a token type to (re)generate its fixture", goldenWriteEnv)
	}

	priv := ed25519.NewKeyFromSeed(goldenSeed[:])
	signer, err := NewSigner(SignerConfig{KeyID: goldenKID, Key: SealKey(priv), TokenType: typ})
	if err != nil {
		t.Fatalf("signer: %v", err)
	}

	issuer, err := NewIssuer(IssuerConfig{
		Signer:     signer,
		Store:      genStore{},
		IssuerName: goldenIssuer,
		DefaultTTL: time.Hour,
		Now:        goldenClock,
	})
	if err != nil {
		t.Fatalf("issuer: %v", err)
	}

	_, token, err := issuer.issue(context.Background(), IssueRequest{
		IssuedBy: Principal{Subject: "test-operator"},
		Subject: Principal{
			Type:     PrincipalTypeAgent,
			TenantID: uuid.MustParse(goldenTenant),
			Subject:  goldenSubject,
			Agent:    &AgentPrincipal{AgentType: "golden", Model: "fixture"},
		},
		Audience: []string{goldenAudience},
		Caveats: Caveats{
			Ops:              []Op{OpGet},
			ResourcePrefixes: []string{"golden/"},
			Requests:         RequestCeiling{Max: 10},
			Budget:           BudgetCeiling{Max: 3 * NanosPerUnit / 2, UnitCode: "USD"}, // 1.5 USD
			Copies:           CopyQuota{Max: goldenCopyQuota},
		},
		TTL:        time.Hour,
		ClientID:   goldenClientIDOf(typ),
		OnBehalfOf: goldenOnBehalfOfOf(typ),
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	out := goldenPath(typ)
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(out, []byte(token), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("wrote %s (%d bytes)", out, len(token))
}

// revLookup revokes nothing, for verifiers over the fixtures.
type revLookup struct{}

func (revLookup) IsRevoked(context.Context, uuid.UUID) (bool, error) { return false, nil }

func (revLookup) IsBiscuitRevoked(context.Context, [][]byte) (bool, error) { return false, nil }

// goldenClientIDOf is the client a fixture of typ names: one only where the
// format carries it.
// goldenOnBehalfOfOf is the principal the fixture of typ acts for: the
// user, in a v7 one, which carries it as sub and act.
func goldenOnBehalfOfOf(typ TokenType) *Principal {
	if typ != TokenTypeV7 {
		return nil
	}
	return &Principal{Type: PrincipalTypeUser, TenantID: uuid.MustParse(goldenTenant), Subject: goldenUser}
}

func goldenClientIDOf(typ TokenType) string {
	if typ == TokenTypeV7 {
		return goldenClientID
	}
	return ""
}
