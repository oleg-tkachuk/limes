package exchangetest

import (
	"errors"
	"sync"
	"testing"
	"uuid"

	"github.com/oleg-tkachuk/limes/exchange"
)

// racingExchanges is how many exchanges of one subject token
// TestOnePerToken races, as replicas would.
const racingExchanges = 8

// OnePerToken is an Exchanger set to issue one capability per subject token
// (exchange.Config.Exchanges), over the consumer's own ExchangeStore, roles
// and row-level security, and how to drive it. Exchanger and Request are
// required; a check whose field is nil is skipped, and says so.
type OnePerToken struct {
	Exchanger *exchange.Exchanger
	// Request is an exchange the Exchanger grants, of a subject token no
	// earlier call returned.
	Request func(t *testing.T) exchange.Request
	// NoJTI is an exchange the Exchanger would grant but that its subject
	// token carries no jti.
	NoJTI func(t *testing.T) exchange.Request
	// Revoke revokes the capability id, as the consumer's operators do.
	Revoke func(t *testing.T, id uuid.UUID)
	// Purge moves the clock past the capabilities' expiry, while the
	// subject tokens Request signs are still live, and purges what expired,
	// as the consumer's housekeeping does: the policy names a TTL shorter
	// than the subject token's.
	Purge func(t *testing.T)
}

// TestOnePerToken holds an Exchanger in one-capability-per-token mode to
// what that mode promises: a subject token exchanged again, or by exchanges
// racing, yields one capability and one token; a revoked one is refused,
// not issued anew; and a token without a jti is refused.
func TestOnePerToken(t *testing.T, env OnePerToken) {
	t.Helper()
	if env.Exchanger == nil || env.Request == nil {
		t.Fatal("exchangetest: OnePerToken.Exchanger and Request required")
	}
	t.Run("exchanged again, the first capability", func(t *testing.T) { testExchangedAgain(t, env) })
	t.Run("racing exchanges converge", func(t *testing.T) { testRacing(t, env) })
	t.Run("a revoked exchange is not issued again", func(t *testing.T) {
		if env.Revoke == nil {
			t.Skip("exchangetest: OnePerToken.Revoke not set")
		}
		testRevoked(t, env)
	})
	t.Run("a revoked exchange is not issued again once purged", func(t *testing.T) {
		if env.Revoke == nil || env.Purge == nil {
			t.Skip("exchangetest: OnePerToken.Revoke or Purge not set")
		}
		testRevokedPurged(t, env)
	})
	t.Run("a token without a jti is refused", func(t *testing.T) {
		if env.NoJTI == nil {
			t.Skip("exchangetest: OnePerToken.NoJTI not set")
		}
		if _, err := env.Exchanger.Exchange(t.Context(), env.NoJTI(t)); !errors.Is(err, exchange.ErrInvalidGrant) {
			t.Errorf("err = %v, want exchange.ErrInvalidGrant", err)
		}
	})
}

func testExchangedAgain(t *testing.T, env OnePerToken) {
	req := env.Request(t)
	first, err := env.Exchanger.Exchange(t.Context(), req)
	if err != nil {
		t.Fatalf("first exchange: %v", err)
	}
	again, err := env.Exchanger.Exchange(t.Context(), req)
	if err != nil {
		t.Fatalf("second exchange: %v", err)
	}
	if again.Capability.ID != first.Capability.ID || again.Token != first.Token || !again.Reissued {
		t.Errorf("exchanged again = %s, reissued %v; want %s and its token", again.Capability.ID, again.Reissued, first.Capability.ID)
	}
}

func testRacing(t *testing.T, env OnePerToken) {
	req := env.Request(t)
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		ids = map[uuid.UUID]int{}
	)
	for range racingExchanges {
		wg.Go(func() {
			res, err := env.Exchanger.Exchange(t.Context(), req)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Errorf("a racing exchange: %v", err)
				return
			}
			ids[res.Capability.ID]++
		})
	}
	wg.Wait()
	if len(ids) != 1 {
		t.Errorf("racing exchanges handed out %d capabilities, want one: %v", len(ids), ids)
	}
}

func testRevoked(t *testing.T, env OnePerToken) {
	req := env.Request(t)
	first, err := env.Exchanger.Exchange(t.Context(), req)
	if err != nil {
		t.Fatalf("first exchange: %v", err)
	}
	env.Revoke(t, first.Capability.ID)
	if _, err := env.Exchanger.Exchange(t.Context(), req); !errors.Is(err, exchange.ErrInvalidGrant) {
		t.Errorf("an exchange of a revoked capability's token: err = %v, want exchange.ErrInvalidGrant", err)
	}
}

func testRevokedPurged(t *testing.T, env OnePerToken) {
	req := env.Request(t)
	first, err := env.Exchanger.Exchange(t.Context(), req)
	if err != nil {
		t.Fatalf("first exchange: %v", err)
	}
	env.Revoke(t, first.Capability.ID)
	env.Purge(t)
	if again, err := env.Exchanger.Exchange(t.Context(), req); !errors.Is(err, exchange.ErrInvalidGrant) {
		t.Errorf("an exchange of a revoked, purged capability's token = %s, %v; want exchange.ErrInvalidGrant", again.Capability.ID, err)
	}
}
