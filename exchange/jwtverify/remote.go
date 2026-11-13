package jwtverify

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/lestrrat-go/httprc/v3"
	"github.com/lestrrat-go/httprc/v3/errsink"
	"github.com/lestrrat-go/jwx/v4/jwk"

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
// limes.ErrInvalidConfig. client fetches it; nil: a client bounded by
// jwks.DefaultFetchTimeout, as jwks.Remote's. Whatever the client's own redirect policy, a
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
		client = defaultKeysClient()
	}
	client = httpsurl.SameOrigin(client)
	// httprc and jwx quote the URL they fetch in their errors, which are
	// logged and returned: they get it without its userinfo, which goes
	// to the server as the Basic auth net/http would derive from it.
	fetched := *u
	if fetched.User != nil {
		client.Transport = userinfoAuth{next: client.Transport, host: u.Host, user: u.User}
		fetched.User = nil
	}
	r := &remote{
		url: fetched.String(), endpoint: u.Redacted(), now: now,
		log:        log.With(slog.String("jwks_url", u.Redacted())),
		refetching: make(chan struct{}, 1),
	}
	ctrl, err := httprc.NewClient(httprc.WithWorkers(remoteWorkers), httprc.WithErrorSink(errsink.NewFunc(r.fetchFailed))).Start(ctx)
	if err != nil {
		return nil, fmt.Errorf("jwtverify: RemoteKeys: %w", err)
	}
	r.ctrl = ctrl
	r.resource, err = httprc.NewResource[jwk.Set](r.url, httprc.TransformFunc[jwk.Set](r.transform),
		httprc.WithHTTPClient(client), httprc.WithMinInterval(remoteRefreshInterval), httprc.WithMaxInterval(remoteRefreshInterval))
	if err != nil {
		return nil, errs.Errorf("%w: jwtverify: RemoteKeys %s: %w", limes.ErrInvalidConfig, r.endpoint, err)
	}
	if err := ctrl.Add(ctx, r.resource, httprc.WithWaitReady(false)); err != nil {
		return nil, errs.Errorf("%w: jwtverify: RemoteKeys %s: %w", limes.ErrInvalidConfig, r.endpoint, err)
	}
	return r, nil
}

// defaultKeysClient is the client RemoteKeys fetches with when given none:
// jwks.Remote's, bounded by jwks.DefaultFetchTimeout, over the default
// transport, so a deployment's proxy settings apply as they do to the
// issuer's own keys.
func defaultKeysClient() *http.Client {
	return &http.Client{Timeout: jwks.DefaultFetchTimeout}
}

// source is the KeySource of r.
func (r *remote) source() *KeySource {
	return &KeySource{keys: r.keys, refetch: r.refetchUnknownKID}
}

// remote is RemoteKeys' key set: the document httprc keeps fetching, and
// when it was last fetched.
type remote struct {
	// url is the URL fetched, without the userinfo RemoteKeys was given,
	// which userinfoAuth sends.
	url string
	// endpoint is the URL RemoteKeys was given with any password masked,
	// the form an error and a log line name.
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

	// refetching holds one token while a token's fetch runs, until the
	// fetch ends whether or not its caller waits; a channel so a caller
	// waiting behind it gives up when its context does.
	refetching chan struct{}
	// lastRefetch is when a token last started a fetch; held by
	// refetching.
	lastRefetch time.Time
}

// transform reads a fetched JWK Set document, no larger than
// jwks.DefaultMaxDocumentBytes, as jwks.Remote caps the issuer's own,
// recording when it was read.
func (r *remote) transform(_ context.Context, res *http.Response) (jwk.Set, error) {
	body, err := io.ReadAll(http.MaxBytesReader(nil, res.Body, jwks.DefaultMaxDocumentBytes))
	if err != nil {
		return nil, fmt.Errorf("jwtverify: reading the JWK Set at %s: %w", r.endpoint, err)
	}
	set, err := jwk.Parse(body)
	if err != nil {
		return nil, fmt.Errorf("jwtverify: parsing the JWK Set at %s: %w", r.endpoint, err)
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
//
// The fetch runs apart from the caller that started it, bounded by
// jwks.DefaultFetchTimeout rather than by the caller's context, as
// jwks.Remote's does: httprc sends a fetch it has taken whatever its
// caller does, so a caller that gives up neither escapes
// remoteMinRefetchInterval nor makes the endpoint look failed; it leaves a
// fetch in flight, which the next caller waits for.
func (r *remote) refetch(ctx context.Context, reason string) (jwk.Set, error) {
	select {
	case r.refetching <- struct{}{}:
	case <-ctx.Done():
		return nil, r.gaveUp(ctx)
	}
	// select picks at random between two ready cases: a caller already
	// done may have won the slot.
	if ctx.Err() != nil {
		<-r.refetching
		return nil, r.gaveUp(ctx)
	}
	now := r.now()
	if !r.lastRefetch.IsZero() && now.Sub(r.lastRefetch) < remoteMinRefetchInterval {
		<-r.refetching
		r.log.DebugContext(ctx, "jwtverify: not fetching the identity provider's keys again so soon", slog.String("reason", reason),
			slog.Time("last_fetch_started", r.lastRefetch))
		return r.heldAfterRefetch()
	}
	r.lastRefetch = now
	r.log.InfoContext(ctx, "jwtverify: fetching the identity provider's keys", slog.String("reason", reason))
	done := make(chan error, 1)
	go func() {
		// The token goes back before the result is sent: a caller woken
		// by it may start the next fetch at once.
		defer func() { <-r.refetching }()
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), jwks.DefaultFetchTimeout)
		defer cancel()
		err := r.ctrl.Refresh(fetchCtx, r.url)
		if err != nil {
			r.failed(err)
			r.log.WarnContext(fetchCtx, "jwtverify: fetching the identity provider's keys failed", slog.String("reason", reason), slog.Any("error", err))
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			return nil, errs.Errorf("%w: jwtverify: fetch the identity provider's keys at %s: %w", jwks.ErrUnavailable, r.endpoint, err)
		}
		return r.current()
	case <-ctx.Done():
		r.log.DebugContext(ctx, "jwtverify: gave up waiting on the identity provider's keys; the fetch runs on", slog.String("reason", reason))
		return nil, r.gaveUp(ctx)
	}
}

// heldAfterRefetch is the set held for a token whose fetch was skipped:
// jwks.ErrUnavailable while the last fetch failed, with none succeeding
// since.
func (r *remote) heldAfterRefetch() (jwk.Set, error) {
	set, err := r.current()
	if err != nil {
		return nil, err
	}
	if fetchErr := r.lastFetchErr(); fetchErr != nil {
		return nil, errs.Errorf("%w: jwtverify: the identity provider's keys at %s could not be fetched again: %w", jwks.ErrUnavailable, r.endpoint, fetchErr)
	}
	return set, nil
}

// gaveUp is the error of a caller whose context ended before the keys it
// waited on were there.
func (r *remote) gaveUp(ctx context.Context) error {
	return errs.Errorf("%w: jwtverify: waiting on the identity provider's keys at %s: %w", jwks.ErrUnavailable, r.endpoint, ctx.Err())
}

// userinfoAuth sends the userinfo of a JWKS URL as Basic auth on each
// request to its host that names no Authorization of its own, as net/http
// does for a URL that carries it, so the URL fetched need not.
type userinfoAuth struct {
	// next sends the request; nil: http.DefaultTransport.
	next http.RoundTripper
	host string
	user *url.Userinfo
}

func (a userinfoAuth) RoundTrip(req *http.Request) (*http.Response, error) {
	next := a.next
	if next == nil {
		next = http.DefaultTransport
	}
	if req.Header.Get("Authorization") != "" || !strings.EqualFold(req.URL.Host, a.host) {
		return next.RoundTrip(req)
	}
	// A RoundTripper must not change the request it is given.
	req = req.Clone(req.Context())
	password, _ := a.user.Password()
	req.SetBasicAuth(a.user.Username(), password)
	return next.RoundTrip(req)
}
