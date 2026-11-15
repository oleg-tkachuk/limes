package exchange

import (
	"log/slog"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes/dpop"
)

// A client's token-request proofs count against a share of the replay
// cache of their own, apart from a capability's, even when the client's id
// spells a capability's: a client cannot use up a capability's share, nor
// a capability a client's.
func TestAClientsReplayShareIsApartFromACapabilitysOfTheSameName(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	replay, err := dpop.NewMemoryReplayCache(dpop.MemoryReplayCacheConfig{EntriesPerHolder: 1, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	if f.ex.cfg.DPoP, err = dpop.NewVerifier(dpop.VerifierConfig{Replay: replay, Now: clock}); err != nil {
		t.Fatal(err)
	}
	// A capability named as the client has used its whole share.
	full := dpop.ReplayEntry{JKT: "another-key", JTI: "another-proof", Holder: client().ID, ExpiresAt: testNow.Add(time.Hour)}
	if _, err := replay.Seen(t.Context(), full); err != nil {
		t.Fatal(err)
	}
	req := request(f.idp.sign(t, typeAccessToken, accessClaims()))
	req.DPoP = proof(t, clientKey(t), "")
	if _, err := f.ex.Exchange(t.Context(), req); err != nil {
		t.Errorf("a client whose id names a capability past its share: %v, want its own share", err)
	}
}
