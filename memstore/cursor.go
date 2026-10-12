package memstore

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
)

// tenantBudgetCursor is memstore's cursor of ListTenantBudgets: a place in
// its order, the last row's ceiling class, utilization, in the decimal text
// it was computed in, and tenant id. Another store's cursor is its own.
type tenantBudgetCursor struct {
	class       ceilingClass
	utilization string
	tenantID    uuid.UUID
}

// tenantBudgetCursorSep separates the cursor's fields; neither a class, a
// decimal nor a uuid contains it.
const tenantBudgetCursorSep = "|"

// tenantBudgetCursorFields is how many fields a cursor has.
const tenantBudgetCursorFields = 3

// plainDecimal is a finite, non-negative decimal: NaN, the infinities or a
// hex float would order differently than the rows they came from.
var plainDecimal = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)

// encode is the cursor as ListTenantBudgets hands it out: opaque, URL-safe.
func (c tenantBudgetCursor) encode() string {
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Join(
		[]string{strconv.Itoa(int(c.class)), c.utilization, c.tenantID.String()}, tenantBudgetCursorSep)))
}

// decodeTenantBudgetCursor reads a cursor encode wrote. Anything else is
// ErrInvalidRequest.
func decodeTenantBudgetCursor(s string) (tenantBudgetCursor, error) {
	bad := fmt.Errorf("%w: memstore: tenant budget cursor %q", limes.ErrInvalidRequest, s)
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return tenantBudgetCursor{}, bad
	}
	fields := strings.Split(string(raw), tenantBudgetCursorSep)
	if len(fields) != tenantBudgetCursorFields {
		return tenantBudgetCursor{}, bad
	}
	class, err := strconv.Atoi(fields[0])
	if err != nil || class < int(classZero) || class > int(classUnlimited) {
		return tenantBudgetCursor{}, bad
	}
	pct, id := fields[1], fields[2]
	if !plainDecimal.MatchString(pct) {
		return tenantBudgetCursor{}, bad
	}
	if _, err := strconv.ParseFloat(pct, 64); err != nil {
		return tenantBudgetCursor{}, bad
	}
	tenant, err := uuid.Parse(id)
	if err != nil {
		return tenantBudgetCursor{}, bad
	}
	return tenantBudgetCursor{class: ceilingClass(class), utilization: pct, tenantID: tenant}, nil
}
