package jwks_test

import (
	"context"
	"crypto/ed25519"
	"net/http/httptest"
	"testing"

	"github.com/oleg-tkachuk/limes/jwks"
	"github.com/oleg-tkachuk/limes/jwks/jwkstest"
)

// contractKID is the kid each resolver of these tests knows.
const contractKID = "contract"

func contractKey(t *testing.T) ed25519.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

func TestStaticContract(t *testing.T) {
	jwkstest.Run(t, func(t *testing.T) jwkstest.Env {
		t.Helper()
		key := contractKey(t)
		return jwkstest.Env{
			Ctx: context.Background(), KID: contractKID, Key: key,
			Resolver: jwks.NewStatic(map[string]ed25519.PublicKey{contractKID: key}),
		}
	})
}

func TestRemoteContract(t *testing.T) {
	jwkstest.Run(t, func(t *testing.T) jwkstest.Env {
		t.Helper()
		key := contractKey(t)
		h, err := jwks.NewHandler(jwks.HandlerConfig{Keys: func() map[string]ed25519.PublicKey {
			return map[string]ed25519.PublicKey{contractKID: key}
		}})
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		remote, err := jwks.NewRemote(jwks.RemoteConfig{URL: srv.URL, Client: srv.Client()})
		if err != nil {
			t.Fatal(err)
		}
		return jwkstest.Env{Ctx: context.Background(), KID: contractKID, Key: key, Resolver: remote}
	})
}
