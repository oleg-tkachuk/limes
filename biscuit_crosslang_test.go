package limes

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes/dpop"
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
	// biscuitSeedCopies is the seed's quota of copies: bounded, so an SDK
	// is checked against a quota that is a number, not only Unlimited.
	biscuitSeedCopies = 8
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

func goldenBiscuitVerifier(t *testing.T) *Verifier {
	t.Helper()
	priv := ed25519.NewKeyFromSeed(goldenSeed[:])
	v, err := NewVerifier(VerifierConfig{
		Keys:           staticKeys(t, map[string]ed25519.PublicKey{goldenKID: priv.Public().(ed25519.PublicKey)}),
		Revocations:    revLookup{},
		TrustedIssuers: []string{goldenIssuer},
		Now:            goldenClock,
		Biscuit:        &BiscuitConfig{Revocations: revLookup{}, MeterCopies: true},
		DPoP:           newVerifier(t, dpop.VerifierConfig{Replay: newReplay(t, dpop.MemoryReplayCacheConfig{}), Now: goldenClock}),
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
	signer, err := NewSigner(SignerConfig{KeyID: goldenKID, Key: SealKey(ed25519.NewKeyFromSeed(goldenSeed[:]))})
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
	cap, _, err := issuer.issue(context.Background(), IssueRequest{
		IssuedBy: Principal{Subject: "test-operator"},
		Subject:  Principal{Type: PrincipalTypeAgent, TenantID: uuid.MustParse(goldenTenant), Subject: goldenSubject},
		Audience: []string{audienceData, audienceMCP},
		Caveats: Caveats{
			Ops: []Op{OpGet, OpList, OpPut, OpShare}, ResourcePrefixes: []string{"corpus/"},
			Requests: RequestCeiling{Unlimited: true}, Budget: BudgetCeiling{Unlimited: true, UnitCode: "USD"},
			Copies: CopyQuota{Max: biscuitSeedCopies},
		},
		TTL: biscuitSeedTTL,
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := issuer.biscuit(context.Background(), asVerified(cap))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(biscuitFixturePath(biscuitSeedFile), []byte(token+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := readBiscuitCase(t)
	jkt, err := dpop.Thumbprint(c.bindKey(t).Public())
	if err != nil || jkt != c.Attenuate.BindJKT {
		t.Fatalf("bind_jkt = %q, the thumbprint of bind_key_seed's key is %q (%v)", c.Attenuate.BindJKT, jkt, err)
	}
	attenuated, err := Attenuate(token, Attenuation{
		Ops:              c.Attenuate.Ops,
		ResourcePrefixes: c.Attenuate.ResourcePrefixes,
		Audience:         c.Attenuate.Planes,
		ExpiresAt:        c.Attenuate.ExpiresAt,
		ConfirmationJKT:  c.Attenuate.BindJKT,
		Requests:         &RequestCeiling{Max: c.Attenuate.MaxRequests},
		Budget:           &BudgetCeiling{Max: Nanos(c.Attenuate.MaxBudget)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(biscuitFixturePath(biscuitAttenuatedFile), []byte(attenuated+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// bindKey is the key the case binds the attenuated copy to, from its seed:
// an SDK signs its DPoP proofs with the same key.
func (c biscuitCase) bindKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	seed, err := hex.DecodeString(c.Attenuate.BindKeySeed)
	if err != nil || len(seed) != ed25519.SeedSize {
		t.Fatalf("bind_key_seed %q is not %d hex bytes: %v", c.Attenuate.BindKeySeed, ed25519.SeedSize, err)
	}
	return ed25519.NewKeyFromSeed(seed)
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
		BindKeySeed      string    `json:"bind_key_seed"`
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
		CopyUnitCode     string    `json:"copy_unit_code"`
		MaxCopies        int64     `json:"max_copies"`
		UnlimitedCopies  bool      `json:"unlimited_copies"`
	} `json:"expect"`
}

// The attenuated seed is accepted, narrowed exactly as the shared case says —
// decoded, not just accepted.
func TestSharedAttenuationVerifies(t *testing.T) {
	c := readBiscuitCase(t)
	v := goldenBiscuitVerifier(t)
	ctx := context.Background()

	seed, err := v.Verify(ctx, VerifyRequest{Token: readBiscuitFixture(t, biscuitSeedFile), Audience: audienceMCP})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	token := readBiscuitFixture(t, biscuitAttenuatedFile)
	key := c.bindKey(t)
	if jkt, err := dpop.Thumbprint(key.Public()); err != nil || jkt != c.Attenuate.BindJKT {
		t.Fatalf("bind_key_seed's thumbprint = %q, %v; want bind_jkt %q", jkt, err, c.Attenuate.BindJKT)
	}
	got, err := v.Verify(ctx, withProof(t.Context(), t, key, token, audienceData, goldenClock()))
	if err != nil {
		t.Fatalf("the attenuated token is refused: %v", err)
	}
	if got.Capability().ID != seed.Capability().ID {
		t.Errorf("ID = %s, want the seed's %s", got.Capability().ID, seed.Capability().ID)
	}
	want := c.Expect
	if !slices.Equal(got.Capability().Caveats.Ops, want.Ops) {
		t.Errorf("Ops = %v, want %v", got.Capability().Caveats.Ops, want.Ops)
	}
	if !slices.Equal(got.Capability().Caveats.ResourcePrefixes, want.ResourcePrefixes) {
		t.Errorf("ResourcePrefixes = %v, want %v", got.Capability().Caveats.ResourcePrefixes, want.ResourcePrefixes)
	}
	if !slices.Equal(got.Capability().Caveats.ResourceURIs, want.ResourceURIs) {
		t.Errorf("ResourceURIs = %v, want %v", got.Capability().Caveats.ResourceURIs, want.ResourceURIs)
	}
	if !slices.Equal(got.Capability().Audience, want.Planes) {
		t.Errorf("Audience = %v, want %v", got.Capability().Audience, want.Planes)
	}
	if !got.Capability().ExpiresAt.Equal(want.ExpiresAt) {
		t.Errorf("ExpiresAt = %v, want %v", got.Capability().ExpiresAt, want.ExpiresAt)
	}
	if got.Capability().ConfirmationJKT != want.ConfirmationJKT {
		t.Errorf("ConfirmationJKT = %q, want %q", got.Capability().ConfirmationJKT, want.ConfirmationJKT)
	}
	if wantCopies := (CopyQuota{Max: want.MaxCopies, Unlimited: want.UnlimitedCopies}); got.Capability().Caveats.Copies != wantCopies {
		t.Errorf("Caveats.Copies = %v, want %v", got.Capability().Caveats.Copies, wantCopies)
	}
	if len(got.Copies()) != 1 || !ceilingIs(got.Copies()[0].Requests, RequestCeiling{Max: want.CopyMaxRequests}) ||
		!ceilingIs(got.Copies()[0].Budget, BudgetCeiling{Max: Nanos(want.CopyMaxBudget), UnitCode: want.CopyUnitCode}) {
		t.Errorf("Copies = %+v, want one with %d requests and %d nanos of %s",
			got.Copies(), want.CopyMaxRequests, want.CopyMaxBudget, want.CopyUnitCode)
	}
	if _, err := v.Verify(ctx, VerifyRequest{Token: token, Audience: audienceMCP}); err == nil {
		t.Error("the plane the attenuation dropped is still accepted")
	}
}
