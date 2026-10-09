package limes

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// The cross-language attenuation (testdata/biscuit/attenuation.json): this
// module mints a seed Biscuit, and an SDK in another language attenuates it
// with the case's arguments; the verifier must accept the result and narrow it
// as the case expects. attenuated.biscuit is the same attenuation made here,
// so the case is checked from this side too; an SDK checks its own against a
// copy of these files.
const (
	biscuitFixtureDir     = "biscuit"
	biscuitSeedFile       = "seed.biscuit"
	biscuitAttenuatedFile = "attenuated.biscuit"
	biscuitCaseFile       = "attenuation.json"
	biscuitWriteEnv       = "LIMES_WRITE_BISCUIT"
	biscuitWriteFlag      = "1"
	biscuitSeedTTL        = time.Hour
)

func biscuitFixturePath(name string) string {
	return filepath.Join(sharedSDKTestdata, biscuitFixtureDir, name)
}

func readBiscuitFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(biscuitFixturePath(name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return strings.TrimSpace(string(raw))
}

func goldenBiscuitVerifier(t *testing.T) *StandardVerifier {
	t.Helper()
	priv := ed25519.NewKeyFromSeed(goldenSeed[:])
	v, err := NewStandardVerifier(VerifierConfig{
		Keys:           NewStaticKeyResolver(map[string]ed25519.PublicKey{goldenKID: priv.Public().(ed25519.PublicKey)}),
		Revocations:    revLookup{},
		TrustedIssuers: []string{goldenIssuer},
		Now:            goldenClock,
		AcceptBiscuit:  true,
		MeterCopies:    true,

		BiscuitRevocations: revLookup{},
	})
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	return v
}

// TestGenerateBiscuitFixtures mints seed.biscuit and attenuates it into
// attenuated.biscuit with the case's arguments. Regenerating the seed
// invalidates every SDK's attenuation of the old one.
func TestGenerateBiscuitFixtures(t *testing.T) {
	if os.Getenv(biscuitWriteEnv) != biscuitWriteFlag {
		t.Skipf("set %s=%s to (re)generate the fixtures", biscuitWriteEnv, biscuitWriteFlag)
	}
	signer, err := NewEd25519Signer(goldenKID, ed25519.NewKeyFromSeed(goldenSeed[:]))
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := NewIssuer(IssuerConfig{
		Signer: signer, Store: genStore{}, IssuerName: goldenIssuer,
		DefaultTTL: biscuitSeedTTL, Now: goldenClock,
	})
	if err != nil {
		t.Fatal(err)
	}
	cap, _, err := issuer.Issue(context.Background(), IssueRequest{
		IssuedBy: Principal{Subject: "test-operator"},
		Subject:  Principal{Type: PrincipalAgent, TenantID: uuid.MustParse(goldenTenant), Subject: goldenSubject},
		Audience: []string{AudiencePlaneData, AudiencePlaneMCP},
		Caveats:  Caveats{Ops: []Op{OpGet, OpList, OpPut}, ResourcePrefixes: []string{"corpus/"}},
		TTL:      biscuitSeedTTL,
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := issuer.Biscuit(cap)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(biscuitFixturePath(biscuitSeedFile), []byte(token+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := readBiscuitCase(t)
	attenuated, err := Attenuate(token, Attenuation{
		Ops:              c.Attenuate.Ops,
		ResourcePrefixes: c.Attenuate.ResourcePrefixes,
		Planes:           c.Attenuate.Planes,
		ExpiresAt:        c.Attenuate.ExpiresAt,
		ConfirmationJKT:  c.Attenuate.BindJKT,
		MaxRequests:      c.Attenuate.MaxRequests,
		MaxBudget:        Nanos(c.Attenuate.MaxBudget),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(biscuitFixturePath(biscuitAttenuatedFile), []byte(attenuated+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readBiscuitCase(t *testing.T) biscuitCase {
	t.Helper()
	var c biscuitCase
	if err := json.Unmarshal([]byte(readBiscuitFixture(t, biscuitCaseFile)), &c); err != nil {
		t.Fatalf("parse case: %v", err)
	}
	return c
}

type biscuitCase struct {
	Attenuate struct {
		Ops              []Op      `json:"ops"`
		ResourcePrefixes []string  `json:"resource_prefixes"`
		Planes           []string  `json:"planes"`
		ExpiresAt        time.Time `json:"expires_at"`
		BindJKT          string    `json:"bind_jkt"`
		MaxRequests      int64     `json:"max_requests"`
		MaxBudget        int64     `json:"max_budget_nanos"`
	} `json:"attenuate"`
	Expect struct {
		Ops              []Op      `json:"ops"`
		ResourcePrefixes []string  `json:"resource_prefixes"`
		ResourceURIs     []string  `json:"resource_uris"`
		Planes           []string  `json:"planes"`
		ExpiresAt        time.Time `json:"expires_at"`
		ConfirmationJKT  string    `json:"confirmation_jkt"`
		CopyMaxRequests  int64     `json:"copy_max_requests"`
		CopyMaxBudget    int64     `json:"copy_max_budget_nanos"`
	} `json:"expect"`
}

// The attenuated seed is accepted, narrowed exactly as the shared case says —
// decoded, not just accepted.
func TestSharedAttenuationVerifies(t *testing.T) {
	c := readBiscuitCase(t)
	v := goldenBiscuitVerifier(t)
	ctx := context.Background()

	seed, err := v.Verify(ctx, readBiscuitFixture(t, biscuitSeedFile), AudiencePlaneMCP)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	token := readBiscuitFixture(t, biscuitAttenuatedFile)
	got, err := v.Verify(ctx, token, AudiencePlaneData)
	if err != nil {
		t.Fatalf("the attenuated token is refused: %v", err)
	}
	if got.ID != seed.ID {
		t.Errorf("ID = %s, want the seed's %s", got.ID, seed.ID)
	}
	want := c.Expect
	if !slices.Equal(got.Caveats.Ops, want.Ops) {
		t.Errorf("Ops = %v, want %v", got.Caveats.Ops, want.Ops)
	}
	if !slices.Equal(got.Caveats.ResourcePrefixes, want.ResourcePrefixes) {
		t.Errorf("ResourcePrefixes = %v, want %v", got.Caveats.ResourcePrefixes, want.ResourcePrefixes)
	}
	if !slices.Equal(got.Caveats.ResourceURIs, want.ResourceURIs) {
		t.Errorf("ResourceURIs = %v, want %v", got.Caveats.ResourceURIs, want.ResourceURIs)
	}
	if !slices.Equal(got.Audience, want.Planes) {
		t.Errorf("Audience = %v, want %v", got.Audience, want.Planes)
	}
	if !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, want.ExpiresAt)
	}
	if got.ConfirmationJKT != want.ConfirmationJKT {
		t.Errorf("ConfirmationJKT = %q, want %q", got.ConfirmationJKT, want.ConfirmationJKT)
	}
	if len(got.Copies) != 1 || got.Copies[0].MaxRequests != want.CopyMaxRequests ||
		got.Copies[0].MaxBudget != Nanos(want.CopyMaxBudget) {
		t.Errorf("Copies = %+v, want one with %d requests and %d nanos",
			got.Copies, want.CopyMaxRequests, want.CopyMaxBudget)
	}
	if _, err := v.Verify(ctx, token, AudiencePlaneMCP); err == nil {
		t.Error("the plane the attenuation dropped is still accepted")
	}
}
