package pgstore

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// textCall is one call that hands the store a text the caller chose.
type textCall struct {
	name string
	call func(ctx context.Context, s string) error
}

// textCalls are calls of every kind that hand pgstore a caller's text, the
// text in the field named, under fx's tenant.
func (fx cursorFixture) textCalls(t testing.TB) []textCall {
	records, err := NewStore(Config{DB: app})
	if err != nil {
		t.Fatal(err)
	}
	subject := meterSubject(fx.capID, fx.tenant)
	capability := func(s string) limes.Capability {
		return limes.Capability{
			ID: uuid.New(), ExpiresAt: fx.now.Add(cursorFuzzTTL),
			Subject: limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: fx.tenant, Subject: "agent" + s},
			Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}, Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true, UnitCode: testUnit}},
		}
	}
	return []textCall{
		{"Insert subject", func(ctx context.Context, s string) error {
			return records.Insert(ctx, limes.InsertRequest{Capability: capability(s)})
		}},
		{"Insert issued by", func(ctx context.Context, s string) error {
			return records.Insert(ctx, limes.InsertRequest{Capability: capability(""), IssuedBy: limes.Principal{Subject: s}, Session: s})
		}},
		{"Revoke note", func(ctx context.Context, s string) error {
			c := capability("")
			if err := records.Insert(ctx, limes.InsertRequest{Capability: c}); err != nil {
				return err
			}
			_, err := records.Revoke(ctx, limes.RevokeRequest{CapabilityID: c.ID, Note: s, Actor: s})
			return err
		}},
		{"ListRecords", func(ctx context.Context, s string) error {
			_, err := fx.census.ListRecords(ctx, limes.ListRecordsRequest{TenantID: fx.tenant, SubjectPrefix: s, ClientID: s, Session: s})
			return err
		}},
		{"CountRecords", func(ctx context.Context, s string) error {
			_, err := fx.census.CountRecords(ctx, limes.CountRecordsRequest{TenantID: fx.tenant, Subject: s, SubjectIssuer: s})
			return err
		}},
		{"RevokeMatching", func(ctx context.Context, s string) error {
			_, err := records.RevokeMatching(ctx, limes.RevokeMatchingRequest{TenantID: fx.tenant, ClientID: "c" + s, Note: s, Actor: s})
			return err
		}},
		{"Charge", func(ctx context.Context, s string) error {
			_, err := fx.usage.Charge(ctx, meter.ChargeRequest{Subject: subject, Amount: 1, Actor: s, Op: limes.Op("x:" + s)}, nil)
			return err
		}},
		{"Reserve", func(ctx context.Context, s string) error {
			_, err := fx.usage.Reserve(ctx, meter.ReserveRequest{Subject: subject, Amount: 1, TTL: cursorFuzzTTL, Actor: s})
			return err
		}},
		{"SumCharges", func(ctx context.Context, s string) error {
			_, err := fx.ledger.SumCharges(ctx, meter.SumChargesRequest{
				TenantID: fx.tenant, From: fx.now.Add(-cursorFuzzPeriod), To: fx.now.Add(cursorFuzzPeriod),
				Filter: meter.ChargeFilter{Actors: []string{"a" + s}, Ops: []limes.Op{limes.Op("x:" + s)}},
			})
			return err
		}},
		{"ReleaseMatching", func(ctx context.Context, s string) error {
			_, err := fx.usage.ReleaseMatching(ctx, meter.ReleaseMatchingRequest{TenantID: fx.tenant, ClientID: "c" + s})
			return err
		}},
	}
}

// Text PostgreSQL cannot keep — a NUL, or bytes that are not UTF-8 — is the
// caller's error, refused as limes.ErrInvalidRequest before a statement
// runs, never a statement failing as the server's.
func TestRequestTextNoStoreCanKeep(t *testing.T) {
	fx := newCursorFixture(t)
	ctx := WithTenant(t.Context(), fx.tenant)
	for _, c := range fx.textCalls(t) {
		if err := c.call(ctx, "plain"); err != nil {
			t.Errorf("%s with plain text: %v", c.name, err)
		}
		for _, s := range []string{cursorFuzzNUL, "a\xffb"} {
			if err := c.call(ctx, s); !errors.Is(err, limes.ErrInvalidRequest) {
				t.Errorf("%s with %q: %v, want limes.ErrInvalidRequest", c.name, s, err)
			}
		}
	}
}

// FuzzRequestText hands every kind of call that passes the store a caller's
// text a string the fuzzer writes, and holds the store to answering it as
// the caller's: accepted, or refused with an error limes answers below 500
// — never one PostgreSQL raises over a value it cannot keep.
func FuzzRequestText(f *testing.F) {
	fx := newCursorFixture(f)
	calls := fx.textCalls(f)
	for _, s := range []string{"", "plain", "naïve", cursorFuzzNUL, "a\xffb", " \t"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		ctx := WithTenant(context.Background(), fx.tenant)
		for _, c := range calls {
			if err := c.call(ctx, s); err != nil && limes.ProblemFor(err).Status >= http.StatusInternalServerError {
				t.Fatalf("%s with %q: %v, answered as the server's", c.name, s, err)
			}
		}
	})
}
