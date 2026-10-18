package pgstore

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oleg-tkachuk/limes"
)

// sqlWidenSession gives a session every tenant by default, as a role or
// database setting (ALTER ROLE … SET limes.all_tenants = 'on') would.
const sqlWidenSession = `SET ` + settingAllTenants + ` = '` + settingOn + `'`

// A scope is the whole of what a call may see: WithTenant, and a call with
// no scope at all, override whatever the session or the role set by default,
// so a default of every tenant does not widen them.
func TestScopeOverridesTheSessionDefault(t *testing.T) {
	f := newTenantFixture(t)
	ctx := context.Background()
	cfg := app.Config().Copy()
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, sqlWidenSession)
		return err
	}
	widened, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(widened.Close)
	records, err := NewStore(Config{DB: widened})
	if err != nil {
		t.Fatal(err)
	}
	for name, scoped := range map[string]context.Context{
		"another tenant's scope": WithTenant(ctx, f.theirs),
		"no scope":               ctx,
	} {
		if r, err := records.GetRecord(scoped, f.child.ID); !errors.Is(err, limes.ErrNotFound) {
			t.Errorf("%s: GetRecord of tenant %s's record = %+v, %v; want ErrNotFound", name, f.mine, r.Capability.ID, err)
		}
	}
	if _, err := records.GetRecord(WithTenant(ctx, f.mine), f.child.ID); err != nil {
		t.Errorf("its own tenant's scope: GetRecord: %v", err)
	}
}
