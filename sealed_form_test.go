package limes

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/limes/dpop"
)

// A form's Unsealed is the token holder's input: its shape is refused
// before any block is read.
func TestCheckUnsealed(t *testing.T) {
	id := []byte("id")
	ids := func(n int) [][]byte {
		out := make([][]byte, n)
		for i := range out {
			out[i] = id
		}
		return out
	}
	for name, tc := range map[string]struct {
		u    Unsealed
		want bool
	}{
		"no blocks":            {Unsealed{RevocationIDs: ids(1)}, true},
		"one block":            {Unsealed{Blocks: make([]Attenuation, 1), RevocationIDs: ids(2)}, true},
		"at the block limit":   {Unsealed{Blocks: make([]Attenuation, MaxBiscuitBlocks), RevocationIDs: ids(MaxBiscuitBlocks + 1)}, true},
		"past the block limit": {Unsealed{Blocks: make([]Attenuation, MaxBiscuitBlocks+1), RevocationIDs: ids(MaxBiscuitBlocks + 2)}, false},
		"an id short":          {Unsealed{Blocks: make([]Attenuation, 1), RevocationIDs: ids(1)}, false},
		"an id over":           {Unsealed{RevocationIDs: ids(2)}, false},
		"no ids":               {Unsealed{}, false},
		"an empty id":          {Unsealed{Blocks: make([]Attenuation, 1), RevocationIDs: [][]byte{id, {}}}, false},
	} {
		if err := checkUnsealed(tc.u); (err == nil) != tc.want {
			t.Errorf("%s: checkUnsealed = %v, want accepted %v", name, err, tc.want)
		}
	}
}

// A block is checked as Attenuate checks one, whatever form decoded it: a
// copy only narrows, and a key-bound token stays bound to its key.
func TestApplyAttenuationRefusesWhatAttenuateWould(t *testing.T) {
	jkt := strings.Repeat("A", 43)
	cur := grant{Capability: Capability{Caveats: Caveats{
		Ops: []Op{OpGet}, Requests: RequestCeiling{Unlimited: true}, Budget: BudgetCeiling{Unlimited: true, UnitCode: "USD"},
	}}}
	bound := cur
	bound.ConfirmationJKT = jkt
	for name, tc := range map[string]struct {
		cur grant
		a   Attenuation
	}{
		"unlimited copy requests": {cur, Attenuation{Requests: &RequestCeiling{Unlimited: true}}},
		"unlimited copy budget":   {cur, Attenuation{Budget: &BudgetCeiling{Unlimited: true}}},
		"a copy budget's unit":    {cur, Attenuation{Budget: &BudgetCeiling{Max: 1, UnitCode: "USD"}}},
		"a binding not a jkt":     {cur, Attenuation{ConfirmationJKT: "key"}},
		"a rebinding":             {bound, Attenuation{ConfirmationJKT: strings.Repeat("B", 43)}},
		"a wider op":              {cur, Attenuation{Ops: []Op{OpPut}}},
	} {
		if _, err := applyAttenuation(tc.cur, tc.a, []byte("block")); err == nil {
			t.Errorf("%s: applied, want refused", name)
		}
	}
	next, err := applyAttenuation(bound, Attenuation{ConfirmationJKT: jkt, Requests: &RequestCeiling{Max: 1}}, []byte("block"))
	if err != nil || next.ConfirmationJKT != jkt || len(next.copies) != 1 || next.copies[0].Requests.Max != 1 {
		t.Errorf("restating the binding with a copy limit = %+v, %v", next, err)
	}
}

// A copy ceiling the form hands back is the verifier's own: changing the
// form's value after does not change the grant.
func TestApplyAttenuationCopiesTheCeilings(t *testing.T) {
	cur := grant{Capability: Capability{Caveats: Caveats{
		Ops: []Op{OpGet}, Requests: RequestCeiling{Unlimited: true}, Budget: BudgetCeiling{Unlimited: true, UnitCode: "USD"},
	}}}
	a := Attenuation{Ops: []Op{OpGet}, Requests: &RequestCeiling{Max: 1}, Budget: &BudgetCeiling{Max: 1}}
	next, err := applyAttenuation(cur, a, []byte("block"))
	if err != nil {
		t.Fatal(err)
	}
	a.Ops[0], a.Requests.Max, a.Budget.Max = OpPut, 9, 9
	if next.Caveats.Ops[0] != OpGet || next.copies[0].Requests.Max != 1 || next.copies[0].Budget.Max != 1 || next.copies[0].Budget.UnitCode != "USD" {
		t.Errorf("the grant changed with the form's value: %+v", next)
	}
}

// form is a TokenForm over the Biscuit form, with what it hands back
// changed by its hooks.
type form struct {
	opened, sealed int
	openErr        error
	unseal         func(Unsealed, error) (Unsealed, error)
	root           func(ed25519.PublicKey) ed25519.PublicKey
}

func (f *form) Seal(ctx context.Context, sign func(context.Context, ed25519.PublicKey) (string, error)) (string, error) {
	f.sealed++
	return biscuitForm{}.Seal(ctx, func(ctx context.Context, root ed25519.PublicKey) (string, error) {
		if f.root != nil {
			root = f.root(root)
		}
		return sign(ctx, root)
	})
}

func (f *form) Open(token string) (string, func(ed25519.PublicKey) (Unsealed, error), error) {
	f.opened++
	if f.openErr != nil {
		return "", nil, f.openErr
	}
	inner, unseal, err := biscuitForm{}.Open(token)
	if err != nil || f.unseal == nil {
		return inner, unseal, err
	}
	return inner, func(root ed25519.PublicKey) (Unsealed, error) { return f.unseal(unseal(root)) }, nil
}

// formFixture is biscuitFixture with f the form of both the issuer and the
// verifier, and a Biscuit of the fixture's capability attenuated once.
func formFixture(t *testing.T, f *form) (*Verifier, string) {
	t.Helper()
	issuer, _, store, pub := buildIssuerVerifier(t)
	issuer.form = f
	v, err := NewVerifier(VerifierConfig{
		Keys:           staticKeys(t, map[string]ed25519.PublicKey{testKID: pub}),
		Revocations:    store,
		TrustedIssuers: []string{"limes-test"},
		Biscuit:        &BiscuitConfig{Revocations: store, Form: f, MeterCopies: true},
		DPoP:           newVerifier(t, dpop.VerifierConfig{Replay: newReplay(t, dpop.MemoryReplayCacheConfig{})}),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, cap, _ := biscuitFixture(t)
	cap.Issuer = issuer.issuerName
	if err := store.Insert(t.Context(), InsertRequest{Capability: cap}); err != nil && !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	token, err := issuer.biscuit(t.Context(), asVerified(cap))
	if err != nil {
		t.Fatal(err)
	}
	narrowed, err := Attenuate(token, Attenuation{Ops: []Op{OpGet}})
	if err != nil {
		t.Fatal(err)
	}
	return v, narrowed
}

// The issuer seals, and the verifier reads, through the form they are
// given.
func TestTheFormIsTheOneConfigured(t *testing.T) {
	f := &form{}
	v, token := formFixture(t, f)
	got, err := v.Verify(t.Context(), VerifyRequest{Token: token, Audience: audienceData})
	if err != nil || !got.FromBiscuit() || len(got.Capability().Caveats.Ops) != 1 {
		t.Fatalf("Verify = %+v, %v", got, err)
	}
	if f.sealed != 1 || f.opened != 1 {
		t.Errorf("the form sealed %d and opened %d tokens, want 1 and 1", f.sealed, f.opened)
	}
}

// What a form hands back is refused when it is not what the holder may
// present: a copy limit past what Attenuate allows, or blocks and ids that
// do not match.
func TestTheVerifierChecksWhatTheFormReads(t *testing.T) {
	for name, tamper := range map[string]func(Unsealed) Unsealed{
		"an unlimited copy": func(u Unsealed) Unsealed {
			u.Blocks[0].Requests = &RequestCeiling{Unlimited: true}
			return u
		},
		"a widening block": func(u Unsealed) Unsealed {
			u.Blocks[0].Ops = []Op{OpPut, OpShare, OpGet, OpList, "admin"}
			return u
		},
		"an id dropped": func(u Unsealed) Unsealed {
			u.RevocationIDs = u.RevocationIDs[:1]
			return u
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := &form{unseal: func(u Unsealed, err error) (Unsealed, error) {
				if err != nil {
					return u, err
				}
				return tamper(u), nil
			}}
			v, token := formFixture(t, f)
			_, err := v.Verify(t.Context(), VerifyRequest{Token: token, Audience: audienceData})
			if !errors.Is(err, ErrInvalidToken) {
				t.Errorf("Verify: err = %v, want ErrInvalidToken", err)
			}
			if name == "a widening block" && !strings.Contains(fmt.Sprint(err), "block 1:") {
				t.Errorf("Verify: err = %v, want it to name block 1, the first after the sealing one", err)
			}
		})
	}
}

// errFormBroke stands in for a form's own failure.
var errFormBroke = errors.New("form broke")

// A form's error is a refusal of the token: ErrBiscuitAttenuation when the
// form says a block is at fault, ErrInvalidToken otherwise, and never the
// form's own sentinels.
func TestAFormsErrorIsARefusal(t *testing.T) {
	for name, tc := range map[string]struct {
		open, unseal error
		block        bool
	}{
		"open fails":          {open: errFormBroke},
		"unseal fails":        {unseal: errFormBroke},
		"a block is refused":  {unseal: fmt.Errorf("%w: %w", ErrBiscuitAttenuation, errFormBroke), block: true},
		"open names a block":  {open: fmt.Errorf("%w: %w", ErrBiscuitAttenuation, errFormBroke), block: true},
		"open asks a request": {open: fmt.Errorf("%w: %w", ErrInvalidRequest, errFormBroke)},
	} {
		t.Run(name, func(t *testing.T) {
			f := &form{}
			v, token := formFixture(t, f)
			f.openErr = tc.open
			if tc.unseal != nil {
				f.unseal = func(Unsealed, error) (Unsealed, error) { return Unsealed{}, tc.unseal }
			}
			_, err := v.Verify(t.Context(), VerifyRequest{Token: token, Audience: audienceData})
			switch {
			case !errors.Is(err, ErrInvalidToken):
				t.Errorf("err = %v, want ErrInvalidToken", err)
			case errors.Is(err, ErrBiscuitAttenuation) != tc.block:
				t.Errorf("err = %v, want ErrBiscuitAttenuation: %v", err, tc.block)
			case errors.Is(err, errFormBroke) || errors.Is(err, ErrInvalidRequest):
				t.Errorf("err = %v carries the form's own sentinels", err)
			}
		})
	}
}

// The issuer refuses to sign a sealed token naming a root key that is no
// Ed25519 public key.
func TestTheIssuerRefusesAShortRoot(t *testing.T) {
	f := &form{root: func(r ed25519.PublicKey) ed25519.PublicKey { return r[:ed25519.PublicKeySize-1] }}
	issuer, _, _, cap, _ := biscuitFixture(t)
	issuer.form = f
	if _, err := issuer.biscuit(t.Context(), asVerified(cap)); err == nil {
		t.Error("a Biscuit was sealed under a short root key")
	}
}

// The root a sealed token names is an Ed25519 public key in base64url, or
// the token is refused.
func TestSealedRoot(t *testing.T) {
	key := make([]byte, ed25519.PublicKeySize)
	for name, tc := range map[string]struct {
		root string
		want bool
	}{
		"a key":         {base64.RawURLEncoding.EncodeToString(key), true},
		"a byte short":  {base64.RawURLEncoding.EncodeToString(key[1:]), false},
		"a byte over":   {base64.RawURLEncoding.EncodeToString(append(key, 0)), false},
		"not base64url": {"!!", false},
		"none":          {"", false},
	} {
		root, err := sealedRoot(&grant{biscuitRoot: tc.root})
		if (err == nil) != tc.want || err != nil && !errors.Is(err, ErrInvalidToken) || tc.want && len(root) != ed25519.PublicKeySize {
			t.Errorf("%s: sealedRoot = %d bytes, %v; want accepted %v", name, len(root), err, tc.want)
		}
	}
}
