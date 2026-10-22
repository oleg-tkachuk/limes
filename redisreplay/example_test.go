package redisreplay_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/oleg-tkachuk/limes/dpop"
	"github.com/oleg-tkachuk/limes/redisreplay"
)

// The request the example signs a proof for.
const (
	exampleMethod = "POST"
	exampleURL    = "https://data.example.com/objects/get"
	exampleToken  = "access-token"
	exampleHolder = "capability-1"
)

// Two verifier replicas share one Redis: a proof one accepted is a replay
// at the other. An in-memory server stands in for Redis here.
func ExampleNew() {
	ctx := context.Background()
	server, err := miniredis.Run()
	if err != nil {
		log.Fatal(err)
	}

	replica := func() *dpop.Verifier {
		cache, err := redisreplay.New(redisreplay.Config{Client: redis.NewClient(&redis.Options{Addr: server.Addr()})})
		if err != nil {
			log.Fatal(err)
		}
		v, err := dpop.NewVerifier(dpop.VerifierConfig{Replay: cache})
		if err != nil {
			log.Fatal(err)
		}
		return v
	}
	first, second := replica(), replica()

	_, key, _ := ed25519.GenerateKey(rand.Reader)
	jkt, _ := dpop.Thumbprint(key.Public())
	proof, err := dpop.Sign(ctx, key, dpop.SignRequest{Method: exampleMethod, URL: exampleURL, Token: exampleToken, IssuedAt: time.Now()})
	if err != nil {
		log.Fatal(err)
	}
	req := dpop.Request{
		Proof: dpop.Proof{JWT: proof, Method: exampleMethod, URL: exampleURL},
		Token: exampleToken, JKT: jkt, Holder: exampleHolder,
	}
	fmt.Println(first.Verify(ctx, req))
	fmt.Println(errors.Is(second.Verify(ctx, req), dpop.ErrReplayed))
	server.Close()
	// Output:
	// <nil>
	// true
}

func ExampleCache_Seen() {
	ctx := context.Background()
	server, err := miniredis.Run()
	if err != nil {
		log.Fatal(err)
	}
	cache, err := redisreplay.New(redisreplay.Config{Client: redis.NewClient(&redis.Options{Addr: server.Addr()})})
	if err != nil {
		log.Fatal(err)
	}

	entry := dpop.ReplayEntry{JKT: "jkt", JTI: "proof-1", Holder: exampleHolder, ExpiresAt: time.Now().Add(time.Minute)}
	fmt.Println(cache.Seen(ctx, entry))
	fmt.Println(cache.Seen(ctx, entry))
	// One sorted set per holder, under the key prefix.
	fmt.Println(server.Keys())
	server.Close()
	// Output:
	// false <nil>
	// true <nil>
	// [limes:dpop:replay:capability-1]
}
