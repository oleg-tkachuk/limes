package limes_test

// Benchmarks of the paths a service runs on every request — verifying and
// authorizing a token, a JWT or a Biscuit, with or without a proof of
// possession — and of minting one, against the in-memory reference store.
// Each reports its allocations; the Parallel ones run the path from every
// processor at once, through one verifier, as a service's handlers do, so
// their ns/op set against the serial one's is how it scales.
//
// The weekly benchmark workflow runs this file on this commit and, copied
// in, on the newest limes release, and compares the two with benchstat
// (.github/ci/bench.ts). It is an external test package and uses the
// exported API alone, so the release compiles it as it is.

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/dpop"
	"github.com/oleg-tkachuk/limes/jwks"
	"github.com/oleg-tkachuk/limes/memstore"
)

const (
	benchIssuer   = "bench-issuer"
	benchAudience = "bench-service"
	benchResource = "corpus/public/report.pdf"
	benchUnit     = "USD"
	benchTTL      = time.Hour
	// benchBlocks is how many attenuation blocks the attenuated Biscuit
	// carries beyond its authority block.
	benchBlocks = 3
	// benchProofMethod and benchProofURL are the request a proof is signed
	// for.
	benchProofMethod = "GET"
	benchProofURL    = "https://bench.example/corpus/public/report.pdf"
	// benchProofIDs is how many proof ids the replay cache remembers, of
	// all holders and of the benchmark's one: past any run's proofs, so a
	// full cache refusing a fresh proof as replayed is never what is timed.
	benchProofIDs = 1 << 22
	// benchCachedIDs is how many capabilities the many-keys cache benchmark
	// asks about.
	benchCachedIDs = 1024
)

// benchEnv is an issuer and a verifier over one in-memory store, and the
// tokens of one capability.
type benchEnv struct {
	issuer   *limes.Issuer
	verifier *limes.Verifier
	cache    *limes.RevocationCache
	tenant   uuid.UUID
	request  limes.IssueRequest
	token    string
	verified limes.VerifiedCapability
}

func newBenchEnv(b *testing.B, bound ed25519.PublicKey) benchEnv {
	b.Helper()
	b.ReportAllocs()
	ctx := context.Background()
	key, err := limes.GenerateKey()
	if err != nil {
		b.Fatal(err)
	}
	records, err := memstore.NewStore(memstore.StoreConfig{})
	if err != nil {
		b.Fatal(err)
	}
	signer, err := limes.NewSigner(limes.SignerConfig{KeyID: key.KeyID, Key: key.Private})
	if err != nil {
		b.Fatal(err)
	}
	issuer, err := limes.NewIssuer(limes.IssuerConfig{Signer: signer, Store: records, IssuerName: benchIssuer})
	if err != nil {
		b.Fatal(err)
	}
	keys, err := jwks.NewStatic(jwks.StaticConfig{Keys: map[string]ed25519.PublicKey{key.KeyID: key.Public()}})
	if err != nil {
		b.Fatal(err)
	}
	cache, err := limes.NewRevocationCache(limes.RevocationCacheConfig{Capabilities: records, Biscuits: records})
	if err != nil {
		b.Fatal(err)
	}
	replay, err := dpop.NewMemoryReplayCache(dpop.MemoryReplayCacheConfig{MaxEntries: benchProofIDs, EntriesPerHolder: benchProofIDs})
	if err != nil {
		b.Fatal(err)
	}
	proofs, err := dpop.NewVerifier(dpop.VerifierConfig{Replay: replay})
	if err != nil {
		b.Fatal(err)
	}
	verifier, err := limes.NewVerifier(limes.VerifierConfig{
		Keys: keys, Revocations: cache, TrustedIssuers: []string{benchIssuer},
		Biscuit: &limes.BiscuitConfig{Revocations: cache}, DPoP: proofs,
	})
	if err != nil {
		b.Fatal(err)
	}
	env := benchEnv{issuer: issuer, verifier: verifier, cache: cache, tenant: uuid.New()}
	env.request = limes.IssueRequest{
		ClientID: exampleClientID,
		IssuedBy: limes.Principal{Type: limes.PrincipalTypeUser, TenantID: env.tenant, Subject: "operator"},
		Subject:  limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: env.tenant, Subject: "agent"},
		Audience: []string{benchAudience},
		TTL:      benchTTL,
		Caveats: limes.Caveats{
			Ops:              []limes.Op{limes.OpGet, limes.OpShare},
			ResourcePrefixes: []string{"corpus/public/"},
			Requests:         limes.RequestCeiling{Max: 1_000_000},
			Budget:           limes.BudgetCeiling{Max: limes.MustParseAmount("1000.00"), UnitCode: benchUnit},
		},
	}
	if bound != nil {
		if env.request.ConfirmationJKT, err = dpop.Thumbprint(bound); err != nil {
			b.Fatal(err)
		}
	}
	issued, err := issuer.Issue(ctx, env.request)
	if err != nil {
		b.Fatal(err)
	}
	env.token = issued.Token
	if bound == nil {
		if env.verified, err = verifier.Verify(ctx, limes.VerifyRequest{Token: env.token, Audience: benchAudience}); err != nil {
			b.Fatal(err)
		}
	}
	return env
}

func BenchmarkIssue(b *testing.B) {
	env := newBenchEnv(b, nil)
	for b.Loop() {
		if _, err := env.issuer.Issue(context.Background(), env.request); err != nil {
			b.Fatal(err)
		}
	}
}

// Minting the Biscuit form of a capability the verifier accepted.
func BenchmarkBiscuit(b *testing.B) {
	env := newBenchEnv(b, nil)
	req := limes.BiscuitRequest{Capability: env.verified}
	for b.Loop() {
		if _, err := env.issuer.Biscuit(context.Background(), req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDelegate(b *testing.B) {
	env := newBenchEnv(b, nil)
	req := limes.DelegateRequest{
		Parent:   env.verified,
		Subject:  limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: env.tenant, Subject: "sub-agent"},
		Audience: []string{benchAudience},
		TTL:      time.Minute,
		Caveats: limes.Caveats{
			Ops:              []limes.Op{limes.OpGet},
			ResourcePrefixes: []string{"corpus/public/2026/"},
			Requests:         limes.RequestCeiling{Max: 10},
			Budget:           limes.BudgetCeiling{Max: limes.MustParseAmount("1.00")},
		},
	}
	for b.Loop() {
		if _, err := env.issuer.Delegate(context.Background(), req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkVerifyJWT(b *testing.B) {
	env := newBenchEnv(b, nil)
	req := limes.VerifyRequest{Token: env.token, Audience: benchAudience}
	for b.Loop() {
		if _, err := env.verifier.Verify(context.Background(), req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAuthorizeJWT(b *testing.B) {
	env := newBenchEnv(b, nil)
	req := limes.AuthorizeRequest{
		Verify: limes.VerifyRequest{Token: env.token, Audience: benchAudience},
		Check:  limes.CheckRequest{Op: limes.OpGet, Resource: benchResource},
	}
	for b.Loop() {
		if _, err := env.verifier.Authorize(context.Background(), req); err != nil {
			b.Fatal(err)
		}
	}
}

// A key-bound capability with its proof: the proof is signed in the loop,
// as each request brings a fresh one, so this counts the client's signature
// with the server's checks.
func BenchmarkVerifyJWTWithDPoP(b *testing.B) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		b.Fatal(err)
	}
	env := newBenchEnv(b, pub)
	ctx := context.Background()
	for b.Loop() {
		proof, err := dpop.Sign(ctx, priv, dpop.SignRequest{Method: benchProofMethod, URL: benchProofURL, Token: env.token, IssuedAt: time.Now()})
		if err != nil {
			b.Fatal(err)
		}
		req := limes.VerifyRequest{Token: env.token, Audience: benchAudience, DPoP: dpop.Proof{JWT: proof, Method: benchProofMethod, URL: benchProofURL}}
		if _, err := env.verifier.Verify(ctx, req); err != nil {
			b.Fatal(err)
		}
	}
}

// benchBiscuit is the Biscuit form of env's capability, attenuated by
// blocks blocks.
func benchBiscuit(b *testing.B, env benchEnv, blocks int) string {
	b.Helper()
	res, err := env.issuer.Biscuit(context.Background(), limes.BiscuitRequest{Capability: env.verified})
	if err != nil {
		b.Fatal(err)
	}
	// Each block narrows the one before: a prefix under its prefix.
	token, prefix := res.Token, "corpus/public/"
	for i := range blocks {
		prefix += strconv.Itoa(i) + "/"
		if token, err = limes.Attenuate(token, limes.Attenuation{ResourcePrefixes: []string{prefix}}); err != nil {
			b.Fatal(err)
		}
	}
	return token
}

func BenchmarkVerifyBiscuit(b *testing.B) {
	env := newBenchEnv(b, nil)
	req := limes.VerifyRequest{Token: benchBiscuit(b, env, 0), Audience: benchAudience}
	for b.Loop() {
		if _, err := env.verifier.Verify(context.Background(), req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkVerifyBiscuitAttenuated(b *testing.B) {
	env := newBenchEnv(b, nil)
	req := limes.VerifyRequest{Token: benchBiscuit(b, env, benchBlocks), Audience: benchAudience}
	for b.Loop() {
		if _, err := env.verifier.Verify(context.Background(), req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAttenuate(b *testing.B) {
	env := newBenchEnv(b, nil)
	token := benchBiscuit(b, env, 0)
	a := limes.Attenuation{ResourcePrefixes: []string{"corpus/public/2026/"}}
	for b.Loop() {
		if _, err := limes.Attenuate(token, a); err != nil {
			b.Fatal(err)
		}
	}
}

// A revocation answer the cache holds.
func BenchmarkRevocationCacheHit(b *testing.B) {
	env := newBenchEnv(b, nil)
	id := env.verified.Capability().ID
	ctx := context.Background()
	if _, err := env.cache.IsRevoked(ctx, id); err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		if _, err := env.cache.IsRevoked(ctx, id); err != nil {
			b.Fatal(err)
		}
	}
}

// The answer to a refused token: its reason and its HTTP response.
func BenchmarkRefusal(b *testing.B) {
	env := newBenchEnv(b, nil)
	ctx := context.Background()
	_, refused := env.verifier.Verify(ctx, limes.VerifyRequest{Token: env.token, Audience: "another-service"})
	if !errors.Is(refused, limes.ErrAudienceMismatch) {
		b.Fatalf("want an audience refusal, got %v", refused)
	}
	for b.Loop() {
		if _, ok := limes.ReasonOf(refused); !ok {
			b.Fatal("no reason")
		}
		_ = env.verifier.ErrorResponse(ctx, refused, limes.AuthSchemeBearer)
	}
}

// parallel runs op from every processor at once until the benchmark ends,
// reporting the first error op returns.
func parallel(b *testing.B, op func() error) {
	b.Helper()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := op(); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

func BenchmarkVerifyJWTParallel(b *testing.B) {
	env := newBenchEnv(b, nil)
	req := limes.VerifyRequest{Token: env.token, Audience: benchAudience}
	parallel(b, func() error {
		_, err := env.verifier.Verify(context.Background(), req)
		return err
	})
}

func BenchmarkAuthorizeJWTParallel(b *testing.B) {
	env := newBenchEnv(b, nil)
	req := limes.AuthorizeRequest{
		Verify: limes.VerifyRequest{Token: env.token, Audience: benchAudience},
		Check:  limes.CheckRequest{Op: limes.OpGet, Resource: benchResource},
	}
	parallel(b, func() error {
		_, err := env.verifier.Authorize(context.Background(), req)
		return err
	})
}

// Every processor's proofs meet in the one replay cache.
func BenchmarkVerifyJWTWithDPoPParallel(b *testing.B) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		b.Fatal(err)
	}
	env := newBenchEnv(b, pub)
	parallel(b, func() error {
		ctx := context.Background()
		proof, err := dpop.Sign(ctx, priv, dpop.SignRequest{Method: benchProofMethod, URL: benchProofURL, Token: env.token, IssuedAt: time.Now()})
		if err != nil {
			return err
		}
		_, err = env.verifier.Verify(ctx, limes.VerifyRequest{Token: env.token, Audience: benchAudience, DPoP: dpop.Proof{JWT: proof, Method: benchProofMethod, URL: benchProofURL}})
		return err
	})
}

func BenchmarkVerifyBiscuitParallel(b *testing.B) {
	env := newBenchEnv(b, nil)
	req := limes.VerifyRequest{Token: benchBiscuit(b, env, benchBlocks), Audience: benchAudience}
	parallel(b, func() error {
		_, err := env.verifier.Verify(context.Background(), req)
		return err
	})
}

// Every processor asks the one cache about the one capability: the cache's
// lock is all they share.
func BenchmarkRevocationCacheHitParallel(b *testing.B) {
	env := newBenchEnv(b, nil)
	id := env.verified.Capability().ID
	if _, err := env.cache.IsRevoked(context.Background(), id); err != nil {
		b.Fatal(err)
	}
	parallel(b, func() error {
		_, err := env.cache.IsRevoked(context.Background(), id)
		return err
	})
}

// Every processor asks about capabilities of its own, as a service's
// handlers serving many callers do.
func BenchmarkRevocationCacheHitParallelManyKeys(b *testing.B) {
	env, ids := benchCachedEnv(b)
	ctx := context.Background()
	var next atomic.Int64
	parallel(b, func() error {
		_, err := env.cache.IsRevoked(ctx, ids[next.Add(1)%benchCachedIDs])
		return err
	})
}

// benchCachedEnv is a bench env whose cache holds benchCachedIDs answers,
// and their ids.
func benchCachedEnv(b *testing.B) (benchEnv, []uuid.UUID) {
	b.Helper()
	env := newBenchEnv(b, nil)
	ids := make([]uuid.UUID, benchCachedIDs)
	for i := range ids {
		ids[i] = uuid.New()
		if _, err := env.cache.IsRevoked(context.Background(), ids[i]); err != nil {
			b.Fatal(err)
		}
	}
	return env, ids
}

// Reading the counts of a cache that holds answers in every shard, as a
// metrics exporter does each time it collects.
func BenchmarkRevocationCacheStats(b *testing.B) {
	env, _ := benchCachedEnv(b)
	for b.Loop() {
		if env.cache.Stats().Capabilities.Entries < benchCachedIDs {
			b.Fatal("the cache lost an answer")
		}
	}
}

// BenchmarkRevocationCacheHitParallelManyKeys with the counts read without
// pause beside it: the gap between the two is what reading them costs the
// checks.
func BenchmarkRevocationCacheHitParallelUnderStats(b *testing.B) {
	env, ids := benchCachedEnv(b)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				env.cache.Stats()
			}
		}
	}()
	b.Cleanup(func() {
		close(stop)
		<-done
	})
	ctx := context.Background()
	var next atomic.Int64
	parallel(b, func() error {
		_, err := env.cache.IsRevoked(ctx, ids[next.Add(1)%benchCachedIDs])
		return err
	})
}

func BenchmarkIssueParallel(b *testing.B) {
	env := newBenchEnv(b, nil)
	parallel(b, func() error {
		_, err := env.issuer.Issue(context.Background(), env.request)
		return err
	})
}
