package limes

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	biscuit "github.com/biscuit-auth/biscuit-go/v2"
	"github.com/google/uuid"
)

// Limits of the capability the copy tests narrow from.
const (
	copyTestCapRequests = 100
	copyTestCapBudget   = 10 * NanosPerUnit
)

// limitedBiscuit is a Biscuit of a capability with request and budget
// limits, and a verifier that admits copy limits.
func limitedBiscuit(t *testing.T) (*StandardVerifier, Capability, string) {
	t.Helper()
	issuer, v, _, _, _ := biscuitFixture(t)
	v.cfg.MeterCopies = true
	cap, _, err := issuer.Issue(context.Background(), IssueRequest{
		IssuedBy: Principal{Subject: "op"},
		Subject:  Principal{Type: PrincipalAgent, TenantID: uuid.New(), Subject: "agent"},
		Audience: []string{audienceData},
		Caveats: Caveats{
			Ops: []Op{OpGet}, Requests: RequestCeiling{Max: copyTestCapRequests},
			Budget: BudgetCeiling{Max: copyTestCapBudget}, UnitCode: DefaultUnitCode,
		},
		TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := issuer.Biscuit(context.Background(), asVerified(cap))
	if err != nil {
		t.Fatal(err)
	}
	return v, cap, token
}

func lastRevocationID(t *testing.T, token string) []byte {
	t.Helper()
	b, err := parseBiscuit(token)
	if err != nil {
		t.Fatal(err)
	}
	ids := b.RevocationIds()
	return ids[len(ids)-1]
}

// A copy's limits arrive as Copies, keyed by the block that set them,
// innermost first — and leave the capability's own caveats as they were,
// since the capability's counters still bound every copy together.
func TestCopyLimitsArriveAsCopies(t *testing.T) {
	v, cap, root := limitedBiscuit(t)
	ctx := context.Background()
	outer, err := Attenuate(root, Attenuation{Requests: &RequestCeiling{Max: 50}, Budget: &BudgetCeiling{Max: 4 * NanosPerUnit}})
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := Attenuate(outer, Attenuation{Ops: []Op{OpGet}}) // sets no limit
	inner, _ := Attenuate(plain, Attenuation{Requests: &RequestCeiling{Max: 10}})

	got, err := v.Verify(ctx, VerifyRequest{Token: inner, Audience: audienceData})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(got.Capability().Copies) != 2 {
		t.Fatalf("copies = %+v, want two: the blocks that set limits", got.Capability().Copies)
	}
	in, out := got.Capability().Copies[0], got.Capability().Copies[1]
	if !bytes.Equal(in.RevocationID, lastRevocationID(t, inner)) || in.Requests != (RequestCeiling{Max: 10}) || !in.Budget.Unlimited {
		t.Errorf("innermost = %+v", in)
	}
	if !bytes.Equal(out.RevocationID, lastRevocationID(t, outer)) || out.Requests != (RequestCeiling{Max: 50}) || out.Budget != (BudgetCeiling{Max: 4 * NanosPerUnit}) {
		t.Errorf("outermost = %+v", out)
	}
	if got.Capability().Caveats.Requests != cap.Caveats.Requests || got.Capability().Caveats.Budget != cap.Caveats.Budget {
		t.Errorf("capability caveats changed: %+v", got.Capability().Caveats)
	}

	// The root Biscuit carries no copy limit; a copy that sets none keeps
	// the ones above it.
	for tok, want := range map[string]int{root: 0, plain: 1} {
		got, err := v.Verify(ctx, VerifyRequest{Token: tok, Audience: audienceData})
		if err != nil || len(got.Capability().Copies) != want {
			t.Errorf("copies = %+v, err = %v; want %d", got, err, want)
		}
	}
}

// A copy limit has to fit every limit in force: the capability's and each
// enclosing copy's. Under no limit at all, any positive value fits.
func TestCopyLimitsNarrow(t *testing.T) {
	v, _, root := limitedBiscuit(t)
	ctx := context.Background()
	outer, _ := Attenuate(root, Attenuation{Requests: &RequestCeiling{Max: 50}, Budget: &BudgetCeiling{Max: 4 * NanosPerUnit}})

	for name, tc := range map[string]struct {
		from string
		a    Attenuation
		ok   bool
	}{
		"requests within the capability's":     {root, Attenuation{Requests: &RequestCeiling{Max: copyTestCapRequests}}, true},
		"requests beyond the capability's":     {root, Attenuation{Requests: &RequestCeiling{Max: copyTestCapRequests + 1}}, false},
		"budget within the capability's":       {root, Attenuation{Budget: &BudgetCeiling{Max: copyTestCapBudget}}, true},
		"budget beyond the capability's":       {root, Attenuation{Budget: &BudgetCeiling{Max: copyTestCapBudget + 1}}, false},
		"requests within the enclosing copy's": {outer, Attenuation{Requests: &RequestCeiling{Max: 50}}, true},
		"requests beyond the enclosing copy's": {outer, Attenuation{Requests: &RequestCeiling{Max: 51}}, false},
		"budget beyond the enclosing copy's":   {outer, Attenuation{Budget: &BudgetCeiling{Max: 4*NanosPerUnit + 1}}, false},
	} {
		t.Run(name, func(t *testing.T) {
			tok, err := Attenuate(tc.from, tc.a)
			if err != nil {
				t.Fatal(err)
			}
			_, err = v.Verify(ctx, VerifyRequest{Token: tok, Audience: audienceData})
			if tc.ok && err != nil {
				t.Fatalf("refused: %v", err)
			}
			if !tc.ok && !errors.Is(err, ErrBiscuitAttenuation) {
				t.Fatalf("err = %v, want ErrBiscuitAttenuation", err)
			}
		})
	}

	// The fixture capability has no limits: any positive copy limit fits.
	_, unlimited, _, _, token := biscuitFixture(t)
	unlimited.cfg.MeterCopies = true
	tok, _ := Attenuate(token, Attenuation{Requests: &RequestCeiling{Max: 1 << 40}, Budget: &BudgetCeiling{Max: MaxNanos}})
	if _, err := unlimited.Verify(ctx, VerifyRequest{Token: tok, Audience: audienceData}); err != nil {
		t.Fatalf("under no limit: %v", err)
	}
}

// Without a Meter that counts copies, a copy limit would go unkept, so the
// token carrying one is refused; a token without one is not affected.
func TestCopyLimitsNeedMeterCopies(t *testing.T) {
	v, _, root := limitedBiscuit(t)
	v.cfg.MeterCopies = false
	ctx := context.Background()
	limited, _ := Attenuate(root, Attenuation{Requests: &RequestCeiling{Max: 5}})
	_, err := v.Verify(ctx, VerifyRequest{Token: limited, Audience: audienceData})
	if !errors.Is(err, ErrCopyCountersNotMetered) || !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("err = %v, want ErrCopyCountersNotMetered", err)
	}
	plain, _ := Attenuate(root, Attenuation{Ops: []Op{OpGet}})
	if _, err := v.Verify(ctx, VerifyRequest{Token: plain, Audience: audienceData}); err != nil {
		t.Fatalf("a copy without limits: %v", err)
	}
}

// Attenuate refuses a negative limit; a block crafted by hand with a zero,
// a negative or a non-integer limit is refused by the verifier.
func TestCopyLimitsMalformed(t *testing.T) {
	v, _, root := limitedBiscuit(t)
	for _, a := range []Attenuation{
		{Requests: &RequestCeiling{Max: -1}}, {Budget: &BudgetCeiling{Max: -1}},
		{Budget: &BudgetCeiling{Max: MaxNanos + 1}},
		// A copy only narrows: a ceiling it lifts means nothing.
		{Requests: &RequestCeiling{Unlimited: true}}, {Budget: &BudgetCeiling{Unlimited: true}},
	} {
		if _, err := Attenuate(root, a); !errors.Is(err, ErrBiscuitAttenuation) {
			t.Errorf("Attenuate(%+v): err = %v", a, err)
		}
	}
	for name, term := range map[string]biscuit.Term{
		"negative": biscuit.Integer(-3),
		"a string": biscuit.String("10"),
	} {
		for _, fact := range []string{biscuitFactMaxRequests, biscuitFactMaxBudget} {
			b, _ := parseBiscuit(root)
			blk := b.CreateBlock()
			if err := blk.AddFact(biscuitFact(fact, term)); err != nil {
				t.Fatal(err)
			}
			next, err := b.Append(rand.Reader, blk.Build())
			if err != nil {
				t.Fatal(err)
			}
			tok, _ := serializeBiscuit(next)
			if _, err := v.Verify(context.Background(), VerifyRequest{Token: tok, Audience: audienceData}); !errors.Is(err, ErrBiscuitAttenuation) {
				t.Errorf("%s %s: err = %v, want ErrBiscuitAttenuation", fact, name, err)
			}
		}
	}
}

// Copies live only on a verified Capability: a token signed from one that
// carries them does not, and a JWT verifies with none.
func TestCopiesAreNeverSigned(t *testing.T) {
	issuer, v, _, cap, _ := biscuitFixture(t)
	withCopies := cap
	withCopies.Copies = []CopyCeiling{{RevocationID: []byte("x"), Requests: RequestCeiling{Max: 1}}}
	jwt, err := issuer.signer.Sign(context.Background(), withCopies)
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.Verify(context.Background(), VerifyRequest{Token: jwt, Audience: audienceData})
	if err != nil {
		t.Fatal(err)
	}
	if got.Capability().Copies != nil {
		t.Fatalf("copies = %+v, want none", got.Capability().Copies)
	}
}

// BiscuitCopy names the limits in force on a copy, innermost first, and does
// so on a verifier that does not meter copies: a limited copy can still be
// read and revoked there.
func TestBiscuitCopyNamesItsLimits(t *testing.T) {
	v, _, root := limitedBiscuit(t)
	v.cfg.MeterCopies = false
	outer, _ := Attenuate(root, Attenuation{Requests: &RequestCeiling{Max: 50}})
	inner, _ := Attenuate(outer, Attenuation{Budget: &BudgetCeiling{Max: NanosPerUnit}})

	got, err := v.BiscuitCopy(context.Background(), inner)
	if err != nil {
		t.Fatalf("BiscuitCopy: %v", err)
	}
	if len(got.Limits) != 2 ||
		!bytes.Equal(got.Limits[0].RevocationID, lastRevocationID(t, inner)) || got.Limits[0].Budget != (BudgetCeiling{Max: NanosPerUnit}) ||
		!bytes.Equal(got.Limits[1].RevocationID, lastRevocationID(t, outer)) || got.Limits[1].Requests != (RequestCeiling{Max: 50}) {
		t.Fatalf("limits = %+v", got.Limits)
	}
	if plain, err := v.BiscuitCopy(context.Background(), root); err != nil || plain.Limits != nil {
		t.Fatalf("root copy: %+v, %v; want no limits", plain, err)
	}
}

// A copy ceiling of 0 freezes the copy: it is a ceiling, not "none".
func TestACopyCanBeFrozen(t *testing.T) {
	_, v, _, _, token := biscuitFixture(t)
	v.cfg.MeterCopies = true
	frozen, err := Attenuate(token, Attenuation{Requests: &RequestCeiling{}, Budget: &BudgetCeiling{}})
	if err != nil {
		t.Fatal(err)
	}
	vc, err := v.Verify(context.Background(), VerifyRequest{Token: frozen, Audience: audienceData})
	if err != nil {
		t.Fatal(err)
	}
	copies := vc.Capability().Copies
	if len(copies) != 1 || copies[0].Requests != (RequestCeiling{}) || copies[0].Budget != (BudgetCeiling{}) {
		t.Errorf("copies = %+v; want one ceiling of 0 requests and 0 spend", copies)
	}
	if copies[0].Requests.Admits(1) || copies[0].Budget.Admits(1) {
		t.Error("a frozen copy admits a request or a spend")
	}
}
