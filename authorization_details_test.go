package limes

import (
	"errors"
	"strings"
	"testing"
)

// A capability's caveats round-trip through RFC 9396 authorization details,
// the operations as the common "actions" field.
func TestAuthorizationDetailsRoundTrip(t *testing.T) {
	c := Caveats{
		Ops: []Op{OpGet, "tool:search"}, ResourcePrefixes: []string{"corpus/"}, MaxRequests: 5,
		MaxBudgetAmount: MustParseAmount("2.5"), UnitCode: "EUR", SourceIPCIDR: []string{"10.0.0.0/8"},
		IdempotencyKeyRequired: true,
	}
	raw, err := MarshalAuthorizationDetails(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"type":"` + AuthorizationDetailsType + `"`, `"actions":["get","tool:search"]`, `"max_budget":"2.5"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("%s lacks %s", raw, want)
		}
	}
	back, err := ParseAuthorizationDetails(raw)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := MarshalAuthorizationDetails(back)
	if string(again) != string(raw) {
		t.Errorf("round trip changed the details:\n%s\n%s", raw, again)
	}
}

func TestParseAuthorizationDetailsIsStrict(t *testing.T) {
	for name, raw := range map[string]string{
		"not an array":   `{"type":"` + AuthorizationDetailsType + `","actions":["get"]}`,
		"empty":          `[]`,
		"another type":   `[{"type":"payment_initiation","actions":["get"]}]`,
		"two entries":    `[{"type":"` + AuthorizationDetailsType + `","actions":["get"]},{"type":"` + AuthorizationDetailsType + `","actions":["put"]}]`,
		"unknown member": `[{"type":"` + AuthorizationDetailsType + `","actions":["get"],"locations":["x"]}]`,
	} {
		if _, err := ParseAuthorizationDetails([]byte(raw)); !errors.Is(err, ErrInvalidCaveats) {
			t.Errorf("%s: err = %v, want ErrInvalidCaveats", name, err)
		}
	}
}
