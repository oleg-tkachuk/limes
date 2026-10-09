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
		Subject:  Principal{TenantID: uuid.New(), Subject: "agent"},
		Audience: []string{AudiencePlaneData},
		Caveats: Caveats{
			Ops: []Op{OpGet}, MaxRequests: copyTestCapRequests,
			MaxBudgetAmount: copyTestCapBudget, UnitCode: DefaultUnitCode,
		},
		TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := issuer.Biscuit(cap)
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
	outer, err := Attenuate(root, Attenuation{MaxRequests: 50, MaxBudget: 4 * NanosPerUnit})
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := Attenuate(outer, Attenuation{Ops: []Op{OpGet}}) // sets no limit
	inner, _ := Attenuate(plain, Attenuation{MaxRequests: 10})

	got, err := v.Verify(ctx, inner, AudiencePlaneData)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(got.Copies) != 2 {
		t.Fatalf("copies = %+v, want two: the blocks that set limits", got.Copies)
	}
	in, out := got.Copies[0], got.Copies[1]
	if !bytes.Equal(in.RevocationID, lastRevocationID(t, inner)) || in.MaxRequests != 10 || in.MaxBudget != 0 {
		t.Errorf("innermost = %+v", in)
	}
	if !bytes.Equal(out.RevocationID, lastRevocationID(t, outer)) || out.MaxRequests != 50 || out.MaxBudget != 4*NanosPerUnit {
		t.Errorf("outermost = %+v", out)
	}
	if got.Caveats.MaxRequests != cap.Caveats.MaxRequests || got.Caveats.MaxBudgetAmount != cap.Caveats.MaxBudgetAmount {
		t.Errorf("capability caveats changed: %+v", got.Caveats)
	}

	// The root Biscuit carries no copy limit; a copy that sets none keeps
	// the ones above it.
	for tok, want := range map[string]int{root: 0, plain: 1} {
		got, err := v.Verify(ctx, tok, AudiencePlaneData)
		if err != nil || len(got.Copies) != want {
			t.Errorf("copies = %+v, err = %v; want %d", got, err, want)
		}
	}
}

// A copy limit has to fit every limit in force: the capability's and each
// enclosing copy's. Under no limit at all, any positive value fits.
func TestCopyLimitsNarrow(t *testing.T) {
	v, _, root := limitedBiscuit(t)
	ctx := context.Background()
	outer, _ := Attenuate(root, Attenuation{MaxRequests: 50, MaxBudget: 4 * NanosPerUnit})

	for name, tc := range map[string]struct {
		from string
		a    Attenuation
		ok   bool
	}{
		"requests within the capability's":       {root, Attenuation{MaxRequests: copyTestCapRequests}, true},
		"requests beyond the capability's":       {root, Attenuation{MaxRequests: copyTestCapRequests + 1}, false},
		"budget within the capability's":         {root, Attenuation{MaxBudget: copyTestCapBudget}, true},
		"budget beyond the capability's":         {root, Attenuation{MaxBudget: copyTestCapBudget + 1}, false},
		"requests within the enclosing copy's":   {outer, Attenuation{MaxRequests: 50}, true},
		"requests beyond the enclosing copy's":   {outer, Attenuation{MaxRequests: 51}, false},
		"budget beyond the enclosing copy's":     {outer, Attenuation{MaxBudget: 4*NanosPerUnit + 1}, false},
		"a budget past what an amount can carry": {root, Attenuation{MaxBudget: MaxNanos + 1}, false},
	} {
		t.Run(name, func(t *testing.T) {
			tok, err := Attenuate(tc.from, tc.a)
			if err != nil {
				t.Fatal(err)
			}
			_, err = v.Verify(ctx, tok, AudiencePlaneData)
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
	tok, _ := Attenuate(token, Attenuation{MaxRequests: 1 << 40, MaxBudget: MaxNanos})
	if _, err := unlimited.Verify(ctx, tok, AudiencePlaneData); err != nil {
		t.Fatalf("under no limit: %v", err)
	}
}

// Without a Meter that counts copies, a copy limit would go unkept, so the
// token carrying one is refused; a token without one is not affected.
func TestCopyLimitsNeedMeterCopies(t *testing.T) {
	v, _, root := limitedBiscuit(t)
	v.cfg.MeterCopies = false
	ctx := context.Background()
	limited, _ := Attenuate(root, Attenuation{MaxRequests: 5})
	_, err := v.Verify(ctx, limited, AudiencePlaneData)
	if !errors.Is(err, ErrCopyCountersNotMetered) || !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("err = %v, want ErrCopyCountersNotMetered", err)
	}
	plain, _ := Attenuate(root, Attenuation{Ops: []Op{OpGet}})
	if _, err := v.Verify(ctx, plain, AudiencePlaneData); err != nil {
		t.Fatalf("a copy without limits: %v", err)
	}
}

// Attenuate refuses a negative limit; a block crafted by hand with a zero,
// a negative or a non-integer limit is refused by the verifier.
func TestCopyLimitsMalformed(t *testing.T) {
	v, _, root := limitedBiscuit(t)
	for _, a := range []Attenuation{{MaxRequests: -1}, {MaxBudget: -1}} {
		if _, err := Attenuate(root, a); !errors.Is(err, ErrBiscuitAttenuation) {
			t.Errorf("Attenuate(%+v): err = %v", a, err)
		}
	}
	for name, term := range map[string]biscuit.Term{
		"zero":     biscuit.Integer(0),
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
			if _, err := v.Verify(context.Background(), tok, AudiencePlaneData); !errors.Is(err, ErrBiscuitAttenuation) {
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
	withCopies.Copies = []CopyCeiling{{RevocationID: []byte("x"), MaxRequests: 1}}
	jwt, err := issuer.signer.Sign(withCopies)
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.Verify(context.Background(), jwt, AudiencePlaneData)
	if err != nil {
		t.Fatal(err)
	}
	if got.Copies != nil {
		t.Fatalf("copies = %+v, want none", got.Copies)
	}
}

// BiscuitCopy names the limits in force on a copy, innermost first, and does
// so on a verifier that does not meter copies: a limited copy can still be
// read and revoked there.
func TestBiscuitCopyNamesItsLimits(t *testing.T) {
	v, _, root := limitedBiscuit(t)
	v.cfg.MeterCopies = false
	outer, _ := Attenuate(root, Attenuation{MaxRequests: 50})
	inner, _ := Attenuate(outer, Attenuation{MaxBudget: NanosPerUnit})

	got, err := v.BiscuitCopy(context.Background(), inner)
	if err != nil {
		t.Fatalf("BiscuitCopy: %v", err)
	}
	if len(got.Limits) != 2 ||
		!bytes.Equal(got.Limits[0].RevocationID, lastRevocationID(t, inner)) || got.Limits[0].MaxBudget != NanosPerUnit ||
		!bytes.Equal(got.Limits[1].RevocationID, lastRevocationID(t, outer)) || got.Limits[1].MaxRequests != 50 {
		t.Fatalf("limits = %+v", got.Limits)
	}
	if plain, err := v.BiscuitCopy(context.Background(), root); err != nil || plain.Limits != nil {
		t.Fatalf("root copy: %+v, %v; want no limits", plain, err)
	}
}
