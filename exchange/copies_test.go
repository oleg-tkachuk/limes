package exchange

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/url"
	"testing"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/jwks"
)

// copyRequests is the request limit the tests' Biscuit copy sets.
const copyRequests = 5

// biscuitVerifier is a verifier of f's capabilities that reads Biscuits,
// counting copies when meter is true.
func biscuitVerifier(t *testing.T, f *fixture, meter bool) *limes.Verifier {
	t.Helper()
	keys, err := jwks.NewStatic(jwks.StaticConfig{Keys: map[string]ed25519.PublicKey{f.signingKID: f.signingKey}})
	if err != nil {
		t.Fatal(err)
	}
	v, err := limes.NewVerifier(limes.VerifierConfig{
		Keys: keys, Revocations: f.store, TrustedIssuers: []string{limesIssuer}, Now: clock,
		Biscuit: &limes.BiscuitConfig{Revocations: f.store, Form: copyForm{}, MeterCopies: meter},
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// limitedCopy is a Biscuit copy, limited to copyRequests, of a capability
// exchanged with no request limit.
func limitedCopy(t *testing.T, f *fixture) string {
	t.Helper()
	f.ex.cfg.Policy = func(ctx context.Context, g Grant) (limes.IssueRequest, error) {
		issue, err := f.readPolicy(ctx, g)
		issue.Caveats.Requests = limes.RequestCeiling{Unlimited: true}
		return issue, err
	}
	res, err := f.ex.Exchange(t.Context(), request(f.idp.sign(t, typeAccessToken, accessClaims())))
	if err != nil {
		t.Fatal(err)
	}
	verified, err := f.verifier.Verify(t.Context(), limes.VerifyRequest{Token: res.Token, Audience: toolResource})
	if err != nil {
		t.Fatal(err)
	}
	form, err := f.ex.cfg.Issuer.Biscuit(t.Context(), limes.BiscuitRequest{Capability: verified})
	if err != nil {
		t.Fatal(err)
	}
	return form.Token
}

// copyForm is a limes.TokenForm whose every token is one copy, limited to
// copyRequests: what introspection answers of a copy is this module's to
// test, the Biscuit encoding the biscuit module's. A token is its root key
// and its sealed JWT, base64url; no signature is checked but the root's.
type copyForm struct{}

// The revocation ids of a copyForm token: the sealing block's, the copy's.
var copyFormIDs = [][]byte{[]byte("sealed"), []byte("copy")}

func (copyForm) Seal(ctx context.Context, sign func(context.Context, ed25519.PublicKey) (string, error)) (string, error) {
	root, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		return "", err
	}
	sealed, err := sign(ctx, root)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(append(root, sealed...)), nil
}

func (copyForm) Open(token string) (string, func(ed25519.PublicKey) (limes.Unsealed, error), error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) <= ed25519.PublicKeySize {
		return "", nil, errors.New("not a copyForm token")
	}
	root, sealed := raw[:ed25519.PublicKeySize], string(raw[ed25519.PublicKeySize:])
	unseal := func(named ed25519.PublicKey) (limes.Unsealed, error) {
		if !bytes.Equal(named, root) {
			return limes.Unsealed{}, errors.New("sealed under another root")
		}
		copyLimit := limes.Attenuation{Requests: &limes.RequestCeiling{Max: copyRequests}}
		return limes.Unsealed{Blocks: []limes.Attenuation{copyLimit}, RevocationIDs: copyFormIDs}, nil
	}
	return sealed, unseal, nil
}

// A Biscuit copy that sets a request limit of its own is answered with that
// limit, by an endpoint whose verifier meters copies, and as inactive by one
// whose verifier does not, as Verify refuses it: never with the capability's
// wider limit.
func TestIntrospectionHoldsACopyToItsLimits(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	copied := limitedCopy(t, f)
	_, body := introspect(t, newIntrospection(t, biscuitVerifier(t, f, true)), url.Values{paramToken: {copied}})
	details, _ := body["authorization_details"].([]any)
	if body["active"] != true || len(details) != 1 {
		t.Fatalf("a metered copy: %v", body)
	}
	entry, _ := details[0].(map[string]any)
	if entry["max_requests"] != float64(copyRequests) || entry["unlimited_requests"] != nil {
		t.Errorf("a copy limited to %d requests is answered %v", copyRequests, entry)
	}
	w, body := introspect(t, newIntrospection(t, biscuitVerifier(t, f, false)), url.Values{paramToken: {copied}})
	if w.Code != 200 || len(body) != 1 || body["active"] != false {
		t.Errorf("an unmetered copy: status %d, %v; want {active: false} alone", w.Code, body)
	}
}

// A copy's limits only narrow the capability's, axis by axis.
func TestEffectiveCaveats(t *testing.T) {
	const capMax, lower, higher = 10, 4, 20
	unit := "USD"
	for name, tc := range map[string]struct {
		caveats  limes.Caveats
		copies   []limes.CopyCeiling
		requests limes.RequestCeiling
		budget   limes.BudgetCeiling
	}{
		"no copy": {
			limes.Caveats{Requests: limes.RequestCeiling{Max: capMax}, Budget: limes.BudgetCeiling{Max: capMax, UnitCode: unit}}, nil,
			limes.RequestCeiling{Max: capMax}, limes.BudgetCeiling{Max: capMax, UnitCode: unit},
		},
		"a lower copy": {
			limes.Caveats{Requests: limes.RequestCeiling{Max: capMax}, Budget: limes.BudgetCeiling{Max: capMax, UnitCode: unit}},
			[]limes.CopyCeiling{{Requests: &limes.RequestCeiling{Max: lower}, Budget: &limes.BudgetCeiling{Max: lower}}},
			limes.RequestCeiling{Max: lower}, limes.BudgetCeiling{Max: lower, UnitCode: unit},
		},
		"a higher copy": {
			limes.Caveats{Requests: limes.RequestCeiling{Max: capMax}, Budget: limes.BudgetCeiling{Max: capMax, UnitCode: unit}},
			[]limes.CopyCeiling{{Requests: &limes.RequestCeiling{Max: higher}, Budget: &limes.BudgetCeiling{Max: higher}}},
			limes.RequestCeiling{Max: capMax}, limes.BudgetCeiling{Max: capMax, UnitCode: unit},
		},
		"an unlimited capability": {
			limes.Caveats{Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true, UnitCode: unit}},
			[]limes.CopyCeiling{{Requests: &limes.RequestCeiling{Max: higher}}, {Requests: &limes.RequestCeiling{Max: lower}}, {Budget: &limes.BudgetCeiling{Max: lower}}},
			limes.RequestCeiling{Max: lower}, limes.BudgetCeiling{Max: lower, UnitCode: unit},
		},
	} {
		got := effectiveCaveats(tc.caveats, tc.copies)
		if got.Requests != tc.requests || got.Budget != tc.budget {
			t.Errorf("%s: requests %+v budget %+v, want %+v %+v", name, got.Requests, got.Budget, tc.requests, tc.budget)
		}
	}
}
