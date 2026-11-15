package pgstore

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oleg-tkachuk/limes/meter"
)

// lookalikeSchema is a schema a session lists before pg_catalog on its
// search path, holding functions named as the catalog's that pgstore's own
// statements call.
const lookalikeSchema = "limes_lookalike"

// lookalikePath is a search path that names lookalikeSchema before
// pg_catalog: an unqualified call to a catalog function resolves to the
// look-alike.
const lookalikePath = lookalikeSchema + ", pg_catalog, public"

// sqlCreateLookalike creates lookalikeSchema with a look-alike of every
// catalog function pgstore's statements call at run time, each failing, so
// that a statement that reached one fails the call.
const sqlCreateLookalike = `
CREATE SCHEMA ` + lookalikeSchema + `;
CREATE FUNCTION ` + lookalikeSchema + `.set_config(text, text, boolean) RETURNS text
  LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'look-alike set_config'; END $$;
CREATE FUNCTION ` + lookalikeSchema + `.current_setting(text) RETURNS text
  LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'look-alike current_setting'; END $$;
CREATE FUNCTION ` + lookalikeSchema + `.current_setting(text, boolean) RETURNS text
  LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'look-alike current_setting'; END $$;
CREATE FUNCTION ` + lookalikeSchema + `.pg_current_xact_id() RETURNS xid8
  LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'look-alike pg_current_xact_id'; END $$;
CREATE FUNCTION ` + lookalikeSchema + `.row_security_active(text) RETURNS boolean
  LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'look-alike row_security_active'; END $$;
GRANT USAGE ON SCHEMA ` + lookalikeSchema + ` TO ` + appUser

// sqlDropLookalike drops lookalikeSchema and its functions.
const sqlDropLookalike = `DROP SCHEMA IF EXISTS ` + lookalikeSchema + ` CASCADE`

// No function a role creates in a schema earlier on a session's search path
// stands in for the catalog functions pgstore's own statements call: a
// scoped charge with a callback, a bump and a scoped read run as they do on
// a path that names pg_catalog first.
func TestALookalikeEarlierOnTheSearchPathStandsInForNoCatalogFunction(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	if _, err := owner.Exec(ctx, sqlCreateLookalike); err != nil {
		t.Fatalf("create the look-alike schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(), sqlDropLookalike); err != nil {
			t.Errorf("drop the look-alike schema: %v", err)
		}
	})
	cfg := app.Config()
	cfg.ConnConfig.RuntimeParams[searchPathParam] = lookalikePath
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	usage, err := NewUsageStore(Config{DB: pool, Now: f.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	subject := meterSubject(f.child.ID, f.mine)
	if _, err := usage.Charge(ctx, meter.ChargeRequest{Subject: subject, Amount: 1}, func(context.Context, meter.Charged) error { return nil }); err != nil {
		t.Errorf("a charge with a callback on a look-alike path: %v", err)
	}
	if _, err := usage.Bump(ctx, meter.BumpRequest{Subject: subject}, nil); err != nil {
		t.Errorf("a bump on a look-alike path: %v", err)
	}
	if _, err := usage.GetUsage(ctx, f.child.ID); err != nil {
		t.Errorf("a scoped read on a look-alike path: %v", err)
	}
}
