package memstore

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"uuid"

	"github.com/oleg-tkachuk/limes"
)

// tenantBudgetCursor is memstore's cursor of ListTenantBudgets: a place in
// its order — the last row's ceiling class, its spend and ceiling, which
// give its utilization exactly, and tenant id. Another store's cursor is its
// own.
type tenantBudgetCursor struct {
	class      ceilingClass
	spent, max limes.Nanos
	tenantID   uuid.UUID
}

// tenantBudgetCursorSep separates the cursor's fields; neither a class, an
// integer nor a uuid contains it.
const tenantBudgetCursorSep = "|"

// tenantBudgetCursorFields is how many fields a cursor has.
const tenantBudgetCursorFields = 4

// encode is the cursor as ListTenantBudgets hands it out: opaque, URL-safe.
func (c tenantBudgetCursor) encode() string {
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Join([]string{
		strconv.Itoa(int(c.class)), strconv.FormatInt(int64(c.spent), 10), strconv.FormatInt(int64(c.max), 10), c.tenantID.String(),
	}, tenantBudgetCursorSep)))
}

// decodeTenantBudgetCursor reads a cursor encode wrote. Anything else is
// ErrInvalidRequest: a class this store does not rank by, a spend or ceiling
// that is negative or past MaxNanos, a bounded row with no ceiling.
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
	c, ok := readTenantBudgetCursor(fields)
	if !ok {
		return tenantBudgetCursor{}, bad
	}
	return c, nil
}

// readTenantBudgetCursor reads a cursor's fields — class, spent, ceiling,
// tenant — refusing any out of range, or a ceiling its class contradicts.
func readTenantBudgetCursor(fields []string) (tenantBudgetCursor, bool) {
	class, err := strconv.Atoi(fields[0])
	if err != nil || class < int(classZero) || class > int(classUnlimited) {
		return tenantBudgetCursor{}, false
	}
	spent, err1 := strconv.ParseInt(fields[1], 10, 64)
	ceiling, err2 := strconv.ParseInt(fields[2], 10, 64)
	tenant, err3 := uuid.Parse(fields[3])
	if err := errors.Join(err1, err2, err3); err != nil ||
		limes.Nanos(spent).Validate() != nil || limes.Nanos(ceiling).Validate() != nil ||
		(ceilingClass(class) == classBounded) != (ceiling > 0) {
		return tenantBudgetCursor{}, false
	}
	return tenantBudgetCursor{class: ceilingClass(class), spent: limes.Nanos(spent), max: limes.Nanos(ceiling), tenantID: tenant}, true
}

// encodeCursor is a cursor as the store hands it out: opaque, URL-safe.
func encodeCursor(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err) // the cursors are structs of uuids, strings, times and integers
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// decodeCursor reads a cursor encodeCursor wrote into v.
func decodeCursor(s string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}
