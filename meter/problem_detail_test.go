package meter

import (
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
)

// The meter's refusals of what a request carries name the field and the
// rule in their problem's detail, and none of the values involved.
func TestMeterProblemDetail(t *testing.T) {
	tenant := uuid.New()
	now := time.Now()
	for name, tc := range map[string]struct {
		err    error
		detail string
	}{
		"From after To":           {SumChargesRequest{TenantID: tenant, From: now, To: now.Add(-time.Hour)}.Validate(), "sum charges: From is not before To"},
		"a negative limit":        {SumChargesRequest{TenantID: tenant, From: now, To: now.Add(time.Hour), Limit: -2}.Validate(), "sum charges: limit is negative"},
		"a negative budget limit": {ListTenantBudgetsRequest{Limit: -3}.Validate(), "list tenant budgets: limit is negative"},
	} {
		p, ok := limes.ProblemOf(tc.err)
		if !ok || p.Detail != tc.detail {
			t.Errorf("%s: %s %q, want %q", name, p.Code, p.Detail, tc.detail)
		}
		if strings.Contains(p.Detail, tenant.String()) {
			t.Errorf("%s: the detail repeats the tenant: %q", name, p.Detail)
		}
	}
}
