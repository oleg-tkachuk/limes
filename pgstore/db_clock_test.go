package pgstore

import (
	"context"
	"testing"
	"time"
)

// The database's clock as a test moves it: a schema whose now() is
// PostgreSQL's shifted by the one row of its shift table, which a session of
// clocked finds before pg_catalog's on its search path. A test cannot move
// the database's own clock, and a store built with Config.Now nil measures
// holds' and leases' expiry by it.
const (
	dbClockSchema = "limes_test_clock"
	// dbClockPath is the search path of a session whose now() is
	// dbClockSchema's.
	dbClockPath = dbClockSchema + `, pg_catalog, "$user", public`
	// searchPathParam is the run-time parameter a session's search path is.
	searchPathParam = "search_path"
)

const (
	// sqlCreateDBClock creates the schema, its shift at zero, and its now(),
	// which appUser may call. STABLE, as pg_catalog.now() is: one value
	// through a statement, and the shift as of that statement.
	sqlCreateDBClock = `
CREATE SCHEMA ` + dbClockSchema + `;
CREATE TABLE ` + dbClockSchema + `.shift (
    one boolean PRIMARY KEY DEFAULT true CHECK (one),
    amount interval NOT NULL);
INSERT INTO ` + dbClockSchema + `.shift (amount) VALUES (interval '0');
CREATE FUNCTION ` + dbClockSchema + `.now() RETURNS timestamptz
LANGUAGE sql STABLE AS $$ SELECT pg_catalog.now() + (SELECT amount FROM ` + dbClockSchema + `.shift) $$;
GRANT USAGE ON SCHEMA ` + dbClockSchema + ` TO ` + appUser + `;
GRANT SELECT ON ` + dbClockSchema + `.shift TO ` + appUser
	// sqlShiftDBClock moves the clock by $1 microseconds, either way.
	sqlShiftDBClock = `UPDATE ` + dbClockSchema + `.shift SET amount = amount + $1::bigint * interval '1 microsecond'`
	// sqlResetDBClock sets the clock back to PostgreSQL's.
	sqlResetDBClock = `UPDATE ` + dbClockSchema + `.shift SET amount = interval '0'`
)

// dbClock is the database's clock as clocked's sessions read it, for one
// test.
type dbClock struct{ t *testing.T }

// newDBClock is the database's clock set back to PostgreSQL's, and set back
// again when t ends.
func newDBClock(t *testing.T) dbClock {
	t.Helper()
	reset := func() error {
		_, err := owner.Exec(context.Background(), sqlResetDBClock)
		return err
	}
	if err := reset(); err != nil {
		t.Fatalf("reset the database's clock: %v", err)
	}
	t.Cleanup(func() {
		if err := reset(); err != nil {
			t.Errorf("reset the database's clock: %v", err)
		}
	})
	return dbClock{t: t}
}

// Shift moves the clock by d, back for a negative d. A failure fails the
// test, which goes on: Shift runs where metertest calls Env.AdvanceExpiry.
func (c dbClock) Shift(d time.Duration) {
	if _, err := owner.Exec(context.Background(), sqlShiftDBClock, d.Microseconds()); err != nil {
		c.t.Errorf("shift the database's clock by %s: %v", d, err)
	}
}

// Now reads the clock, as a statement of clocked's sees it.
func (c dbClock) Now() time.Time {
	var now time.Time
	if err := clocked.QueryRow(context.Background(), sqlNow).Scan(&now); err != nil {
		c.t.Errorf("read the database's clock: %v", err)
	}
	return now.UTC()
}
