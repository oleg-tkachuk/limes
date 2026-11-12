package jwtverify

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/lestrrat-go/httprc/v3"
	"github.com/lestrrat-go/httprc/v3/errsink"
	"github.com/lestrrat-go/jwx/v3/jwk"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/errs"
	"github.com/oleg-tkachuk/limes/internal/httpsurl"
	"github.com/oleg-tkachuk/limes/jwks"
)

// The intervals of RemoteKeys, those a limes verifier's jwks.Remote keeps
// by default for the issuer's own keys, so the two fail alike: both answer
// an unknown kid as unavailable while fetches fail.
const (
	// remoteRefreshInterval is how often the key set is fetched, whatever
	// the identity provider's Cache-Control says: a set fetched less
	// often would go past remoteMaxStale while the endpoint is well.
	remoteRefreshInterval = jwks.DefaultRefreshInterval
	// remoteMinRefetchInterval is the shortest time between two fetches a
	// token starts, its kid unknown or no set held: a stream of tokens
	// with made-up kids fetches at most once per this interval.
	remoteMinRefetchInterval = jwks.DefaultMinRefreshInterval
	// remoteMaxStale is how long the last fetched set is served while
	// fetches fail; past it the keys are jwks.ErrUnavailable.
	remoteMaxStale = jwks.DefaultMaxStale
)

// remoteWorkers is how many fetches of the key set may run at once: it is
// one document.
const remoteWorkers = 1

// The reasons a token starts a fetch, as logged.
const (
	reasonUnknownKID = "unknown kid"
	reasonNoKeys     = "no usable key set"
)

// RemoteKeys is a KeySource of the JWK Set at rawURL, fetched in the
// background every few minutes until ctx is done. rawURL must be an https
// URL with a host: over anything else whoever sits on the path chooses the
// keys, and so the subject tokens the exchange accepts; any other is
// limes.ErrInvalidConfig. client fetches it; nil: jwk.DefaultHTTPClient,
// with a request timeout. Whatever the client's own redirect policy, a
// redirect to another scheme or host is refused, on a copy of the client:
// the caller's is left as it is. It does not wait for the first fetch. A
// token naming a kid the set does not hold, or arriving while no usable set
// is held, fetches it again at once, at most once per
// jwks.DefaultMinRefreshInterval. While the last fetch failed, with none
// succeeding since, such a kid is an error wrapping jwks.ErrUnavailable
// rather than a refused token: it may be a key rotated in meanwhile. The last
// set fetched is served while rawURL is out of reach, for at most
// jwks.DefaultMaxStale; never fetched or past that, the keys are an error
// wrapping jwks.ErrUnavailable, the server's. Fetches a token starts, and
// every failed fetch, are logged to slog.Default().
func RemoteKeys(ctx context.Context, rawURL string, client *http.Client) (*KeySource, error) {
	r, err := newRemote(ctx, rawURL, client, time.Now, slog.Default())
	if err != nil {
		return nil, err
	}
	return r.source(), nil
}

// newRemote is RemoteKeys' key set on the clock now, logging to log.
func newRemote(ctx context.Context, rawURL string, client *http.Client, now func() time.Time, log *slog.Logger) (*remote, error) {
	if rawURL == "" {
		return nil, fmt.Errorf("%w: jwtverify: RemoteKeys: url required", limes.ErrInvalidConfig)
	}
	u, err := parseKeysURL(rawURL)
	if err != nil {
		log.WarnContext(ctx, "jwtverify: refused the identity provider's JWKS URL", slog.Any("error", err))
		return nil, err
	}
	if client == nil {
		client = jwk.DefaultHTTPClient()
	}
	client = httpsurl.SameOrigin(client)
	r := &remote{
		url: rawURL, endpoint: u.Redacted(), now: now,
		log:        log.With(slog.String("jwks_url", u.Redacted())),
		refetching: make(chan struct{}, 1),
	}
	ctrl, err := httprc.NewClient(httprc.WithWorkers(remoteWorkers), httprc.WithErrorSink(errsink.NewFunc(r.fetchFailed))).Start(ctx)
	if err != nil {
		return nil, fmt.Errorf("jwtverify: RemoteKeys: %w", err)
	}
	r.ctrl = ctrl
	r.resource, err = httprc.NewResource[jwk.Set](rawURL, httprc.TransformFunc[jwk.Set](r.transform),
		httprc.WithHTTPClient(client), httprc.WithMinInterval(remoteRefreshInterval), httprc.WithMaxInterval(remoteRefreshInterval))
	if err != nil {
		return nil, errs.Errorf("%w: jwtverify: RemoteKeys %s: %w", limes.ErrInvalidConfig, r.endpoint, err)
	}
	if err := ctrl.Add(ctx, r.resource, httprc.WithWaitReady(false)); err != nil {
		return nil, errs.Errorf("%w: jwtverify: RemoteKeys %s: %w", limes.ErrInvalidConfig, r.endpoint, err)
	}
	return r, nil
}

// source is the KeySource of r.
func (r *remote) source() *KeySource {
	return &KeySource{keys: r.keys, refetch: r.refetchUnknownKID}
}

// remote is RemoteKeys' key set: the document httprc keeps fetching, and
// when it was last fetched.
type remote struct {
	url string
	// endpoint is url with any password masked, the form an error and a
	// log line name.
	endpoint string
	ctrl     httprc.Controller
	resource *httprc.ResourceBase[jwk.Set]
	now      func() time.Time
	log      *slog.Logger

	mu        sync.Mutex
	fetchedAt time.Time // when a set was last fetched and read; zero for never
	// fetchErr is why the last fetch failed, nil once one has succeeded
	// since: while it is set, a kid the held set lacks may be one the
	// identity provider rotated in, so it is not refused as unknown.
	fetchErr error

	// refetching holds one token while a token's fetch runs, a channel so a
	// caller waiting behind it gives up when its context does.
	refetching chan struct{}
	// lastRefetch is when a token last started a fetch; held by
	// refetching.
	lastRefetch time.Time
}

// transform reads a fetched JWK Set document, recording when it was read.
func (r *remote) transform(ctx context.Context, res *http.Response) (jwk.Set, error) {
	set, err := (&jwk.Transformer{}).Transform(ctx, res)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.fetchedAt = r.now()
	r.fetchErr = nil
	r.mu.Unlock()
	return set, nil
}

// failed records err as why the last fetch failed.
func (r *remote) failed(err error) {
	r.mu.Lock()
	r.fetchErr = err
	r.mu.Unlock()
}

// lastFetchErr is why the last fetch failed, nil when it succeeded.
func (r *remote) lastFetchErr() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fetchErr
}

// fetchFailed logs a background fetch that failed, and the set served
// meanwhile, if any.
func (r *remote) fetchFailed(ctx context.Context, err error) {
	r.failed(err)
	fetchedAt := r.lastFetched()
	if fetchedAt.IsZero() {
		r.log.WarnContext(ctx, "jwtverify: fetching the identity provider's keys failed; none held", slog.Any("error", err))
		return
	}
	r.log.WarnContext(ctx, "jwtverify: fetching the identity provider's keys failed; serving the last set fetched",
		slog.Time("fetched_at", fetchedAt), slog.Time("served_until", fetchedAt.Add(remoteMaxStale)), slog.Any("error", err))
}

// lastFetched is when a set was last fetched and read, zero for never.
func (r *remote) lastFetched() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fetchedAt
}

// keys is the set held, fetched again first when none usable is.
func (r *remote) keys(ctx context.Context) (jwk.Set, error) {
	if set, err := r.current(); err == nil {
		return set, nil
	}
	return r.refetch(ctx, reasonNoKeys)
}

// refetchUnknownKID is the set fetched again for a token naming a kid the
// set held does not.
func (r *remote) refetchUnknownKID(ctx context.Context) (jwk.Set, error) {
	return r.refetch(ctx, reasonUnknownKID)
}

// current is the set held, refused when there is none or it is older than
// remoteMaxStale.
func (r *remote) current() (jwk.Set, error) {
	set := r.resource.Resource()
	fetchedAt := r.lastFetched()
	if set == nil || fetchedAt.IsZero() {
		return nil, fmt.Errorf("%w: jwtverify: the identity provider's keys at %s were never fetched", jwks.ErrUnavailable, r.endpoint)
	}
	if age := r.now().Sub(fetchedAt); age > remoteMaxStale {
		return nil, fmt.Errorf("%w: jwtverify: the identity provider's keys at %s were fetched %s ago, past %s", jwks.ErrUnavailable, r.endpoint, age, remoteMaxStale)
	}
	return set, nil
}

// refetch fetches the set again for a token, unless a token did within
// remoteMinRefetchInterval, and is the set then held. One fetch runs at a
// time: a caller arriving during one waits for it and takes its set. While
// the last fetch failed, with none succeeding since, it is
// jwks.ErrUnavailable even when the fetch is skipped, as jwks.Remote is: a
// token's kid the held set lacks may be one rotated in meanwhile, and
// refusing the token would blame it for the endpoint's failure.
func (r *remote) refetch(ctx context.Context, reason string) (jwk.Set, error) {
	select {
	case r.refetching <- struct{}{}:
	case <-ctx.Done():
		return nil, errs.Errorf("%w: jwtverify: waiting on the identity provider's keys at %s: %w", jwks.ErrUnavailable, r.endpoint, ctx.Err())
	}
	defer func() { <-r.refetching }()
	now := r.now()
	if !r.lastRefetch.IsZero() && now.Sub(r.lastRefetch) < remoteMinRefetchInterval {
		r.log.DebugContext(ctx, "jwtverify: not fetching the identity provider's keys again so soon", slog.String("reason", reason),
			slog.Time("last_fetch_started", r.lastRefetch))
		set, err := r.current()
		if err != nil {
			return nil, err
		}
		if fetchErr := r.lastFetchErr(); fetchErr != nil {
			return nil, errs.Errorf("%w: jwtverify: the identity provider's keys at %s could not be fetched again: %w", jwks.ErrUnavailable, r.endpoint, fetchErr)
		}
		return set, nil
	}
	r.lastRefetch = now
	r.log.InfoContext(ctx, "jwtverify: fetching the identity provider's keys", slog.String("reason", reason))
	if err := r.ctrl.Refresh(ctx, r.url); err != nil {
		if ctx.Err() == nil {
			// A caller giving up is no failure of the endpoint's.
			r.failed(err)
		}
		r.log.WarnContext(ctx, "jwtverify: fetching the identity provider's keys failed", slog.String("reason", reason), slog.Any("error", err))
		return nil, errs.Errorf("%w: jwtverify: fetch the identity provider's keys at %s: %w", jwks.ErrUnavailable, r.endpoint, err)
	}
	return r.current()
}
