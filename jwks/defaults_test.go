package jwks

import (
	"testing"
	"time"
)

// A zero RemoteConfig field takes the Default* constant its documentation
// names.
func TestRemoteConfigDefaults(t *testing.T) {
	r, err := NewRemote(RemoteConfig{URL: "https://issuer.example.com/jwks"})
	if err != nil {
		t.Fatal(err)
	}
	if r.cfg.Client.Timeout != 10*time.Second || r.cfg.RefreshInterval != 5*time.Minute ||
		r.cfg.MinRefreshInterval != 10*time.Second || r.cfg.MaxStale != time.Hour || r.cfg.MaxDocumentBytes != 1<<20 {
		t.Errorf("remote JWKS defaults = timeout %s, refresh %s, min refresh %s, max stale %s, max bytes %d",
			r.cfg.Client.Timeout, r.cfg.RefreshInterval, r.cfg.MinRefreshInterval, r.cfg.MaxStale, r.cfg.MaxDocumentBytes)
	}

	if DefaultFetchTimeout != 10*time.Second || DefaultRefreshInterval != 5*time.Minute ||
		DefaultMinRefreshInterval != 10*time.Second || DefaultMaxStale != time.Hour || DefaultMaxDocumentBytes != 1<<20 {
		t.Error("a Default* constant moved from what the documentation states")
	}
}
