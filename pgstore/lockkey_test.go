package pgstore

import (
	"context"
	"testing"
	"uuid"
)

// collisionSearch is how many tenant ids the test draws to find two whose
// 32-bit hashtext meets: the birthday bound puts a pair well within it.
const collisionSearch = 400_000

// sqlTryTenantLock tries the tenant lock sqlTakeTenantLock takes, without
// waiting.
const sqlTryTenantLock = `SELECT pg_try_advisory_xact_lock(` + tenantLockKey + `)`

// sqlHashtextCollision is two distinct ids of collisionSearch drawn whose
// hashtext is the same.
const sqlHashtextCollision = `
SELECT a, b FROM (
  SELECT min(id::text)::uuid AS a, max(id::text)::uuid AS b
  FROM (SELECT gen_random_uuid() AS id FROM generate_series(1, $1)) ids
  GROUP BY hashtext(id::text) HAVING count(*) > 1
) pairs LIMIT 1`

// Two tenants whose ids share a 32-bit hash still take tenant locks of their
// own: one tenant's revoke never waits on another's.
func TestTenantLocksOfCollidingTenantsAreApart(t *testing.T) {
	var a, b uuid.UUID
	if err := owner.QueryRow(t.Context(), sqlHashtextCollision, collisionSearch).Scan(&a, &b); err != nil {
		t.Fatalf("no colliding pair in %d ids: %v", collisionSearch, err)
	}
	held, err := owner.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Rollback(context.WithoutCancel(t.Context())) }()
	if _, err := held.Exec(t.Context(), sqlTakeTenantLock, revokeLockClass, a); err != nil {
		t.Fatal(err)
	}
	other, err := owner.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Rollback(context.WithoutCancel(t.Context())) }()
	var got bool
	if err := other.QueryRow(t.Context(), sqlTryTenantLock, revokeLockClass, b).Scan(&got); err != nil || !got {
		t.Errorf("tenant %s waits on tenant %s's lock (%v)", b, a, err)
	}
	if err := other.QueryRow(t.Context(), sqlTryTenantLock, eraseLockClass, a).Scan(&got); err != nil || !got {
		t.Errorf("the erase lock of tenant %s waits on its revoke lock (%v)", a, err)
	}
	if err := other.QueryRow(t.Context(), sqlTryTenantLock, revokeLockClass, a).Scan(&got); err != nil || got {
		t.Errorf("tenant %s's revoke lock was taken twice (%v)", a, err)
	}
}
