package jwks_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"

	"github.com/oleg-tkachuk/limes/jwks"
)

// jwksPath is where an issuer serves its key set.
const jwksPath = "/.well-known/jwks.json"

func newPublicKey() ed25519.PublicKey {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	return pub
}

func ExampleNewStatic() {
	ctx := context.Background()
	pub := newPublicKey()
	kid := jwks.KeyID(pub)
	keys, err := jwks.NewStatic(jwks.StaticConfig{Keys: map[string]ed25519.PublicKey{kid: pub}})
	if err != nil {
		log.Fatal(err)
	}
	got, err := keys.PublicKey(ctx, kid)
	fmt.Println(got.Equal(pub), err)

	_, err = keys.PublicKey(ctx, "unknown")
	fmt.Println(errors.Is(err, jwks.ErrUnknownKID))
	// Output:
	// true <nil>
	// true
}

// Rotation: publish the new key, sign with it once every verifier has it,
// and withdraw the old one only after the longest TTL signed under it.
func ExampleStatic_SetKey() {
	ctx := context.Background()
	oldKey, newKey := newPublicKey(), newPublicKey()
	keys, err := jwks.NewStatic(jwks.StaticConfig{Keys: map[string]ed25519.PublicKey{"2026-01": oldKey}})
	if err != nil {
		log.Fatal(err)
	}
	if err := keys.SetKey("2026-04", newKey); err != nil {
		log.Fatal(err)
	}
	fmt.Println(len(keys.Keys()))

	keys.RemoveKey("2026-01")
	_, err = keys.PublicKey(ctx, "2026-01")
	fmt.Println(len(keys.Keys()), errors.Is(err, jwks.ErrUnknownKID))
	// Output:
	// 2
	// 1 true
}

func ExampleMarshal() {
	pub := newPublicKey()
	doc, err := jwks.Marshal(map[string]ed25519.PublicKey{"k1": pub})
	if err != nil {
		log.Fatal(err)
	}
	keys, err := jwks.Parse(doc)
	fmt.Println(err, keys["k1"].Equal(pub))

	// A document with no "keys" array is an error; an empty one withdraws all.
	_, err = jwks.Parse([]byte(`{}`))
	fmt.Println(err != nil)
	keys, err = jwks.Parse([]byte(`{"keys":[]}`))
	fmt.Println(len(keys), err)
	// Output:
	// <nil> true
	// true
	// 0 <nil>
}

func ExampleNewHandler() {
	ctx := context.Background()
	pub := newPublicKey()
	keys, err := jwks.NewStatic(jwks.StaticConfig{Keys: map[string]ed25519.PublicKey{"k1": pub}})
	if err != nil {
		log.Fatal(err)
	}
	// The issuer serves what its Static holds, so a rotation is served at once.
	handler, err := jwks.NewHandler(jwks.HandlerConfig{Keys: keys.Keys})
	if err != nil {
		log.Fatal(err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, jwksPath, nil))
	fmt.Println(rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control"))

	// A client holding the same set revalidates for nothing.
	again := httptest.NewRequestWithContext(ctx, http.MethodGet, jwksPath, nil)
	again.Header.Set("If-None-Match", rec.Header().Get("ETag"))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, again)
	fmt.Println(rec.Code)
	// Output:
	// 200 application/jwk-set+json public, max-age=300
	// 304
}

func ExampleNewRemote() {
	// A verifier apart from the issuer fetches its set over HTTPS. NewRemote
	// does not fetch: the first PublicKey call does.
	remote, err := jwks.NewRemote(jwks.RemoteConfig{URL: "https://issuer.example/.well-known/jwks.json"})
	fmt.Println(remote != nil, err)

	_, err = jwks.NewRemote(jwks.RemoteConfig{URL: "http://issuer.example/.well-known/jwks.json"})
	fmt.Println(err != nil)
	// Output:
	// true <nil>
	// true
}

func ExampleRemote_PublicKey() {
	ctx := context.Background()
	pub := newPublicKey()
	handler, err := jwks.NewHandler(jwks.HandlerConfig{Keys: func() map[string]ed25519.PublicKey {
		return map[string]ed25519.PublicKey{"k1": pub}
	}})
	if err != nil {
		log.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)

	remote, err := jwks.NewRemote(jwks.RemoteConfig{URL: server.URL, Client: server.Client()})
	if err != nil {
		log.Fatal(err)
	}
	got, err := remote.PublicKey(ctx, "k1")
	fmt.Println(got.Equal(pub), err)
	server.Close()
	// Output:
	// true <nil>
}

func ExampleKeyID() {
	pub := newPublicKey()
	kid := jwks.KeyID(pub)
	// The same key always gets the same kid, wherever it is derived.
	fmt.Println(kid == jwks.KeyID(bytes.Clone(pub)), len(kid))
	// Output:
	// true 43
}
