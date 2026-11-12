package jwtverify

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// urlUser is the user a JWKS URL carries in its userinfo, beside
// urlPassword.
const urlUser = "jwks-reader"

// notAJWKSet is a body that is no JWK Set, as a maintenance page is.
const notAJWKSet = "<html>down for maintenance</html>"

// lockedBuffer is a buffer a logger writes to from several goroutines.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// withUserinfo is srvURL with urlUser and urlPassword as its userinfo,
// under scheme.
func withUserinfo(srvURL, scheme string) string {
	_, rest, _ := strings.Cut(srvURL, "://")
	return scheme + "://" + urlUser + ":" + urlPassword + "@" + rest
}

// A JWKS URL's password reaches the identity provider as Basic auth, and
// no log line or error of a failed fetch, whatever the endpoint answered:
// the URL the fetch errors quote carries no userinfo.
func TestRemoteKeysKeepTheURLPasswordOutOfFailures(t *testing.T) {
	answers := map[string]func(http.ResponseWriter){
		"an error status": func(w http.ResponseWriter) { http.Error(w, "down", http.StatusServiceUnavailable) },
		"no JWK Set": func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", jsonContentType)
			_, _ = w.Write([]byte(notAJWKSet))
		},
	}
	for answer, answerWith := range answers {
		for _, scheme := range []string{"https", "HTTPS"} {
			t.Run(answer+" over "+scheme, func(t *testing.T) {
				var gotAuth, wrongAuth atomic.Int64
				srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					user, password, ok := r.BasicAuth()
					if ok && user == urlUser && password == urlPassword {
						gotAuth.Add(1)
					} else {
						wrongAuth.Add(1)
					}
					answerWith(w)
				}))
				t.Cleanup(srv.Close)
				logs := &lockedBuffer{}
				r, err := newRemote(t.Context(), withUserinfo(srv.URL, scheme), srv.Client(), time.Now, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
				if err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(remoteKeysWait)
				for r.lastFetchErr() == nil {
					if time.Now().After(deadline) {
						t.Fatal("the background fetch never failed")
					}
					time.Sleep(remoteKeysPoll)
				}
				_, err = r.keys(t.Context())
				if err == nil {
					t.Fatal("keys from an endpoint that fails: no error")
				}
				if strings.Contains(err.Error(), urlPassword) {
					t.Errorf("the keys' error names the URL's password: %v", err)
				}
				if strings.Contains(logs.String(), urlPassword) {
					t.Errorf("the log names the URL's password:\n%s", logs)
				}
				if gotAuth.Load() == 0 || wrongAuth.Load() != 0 {
					t.Errorf("fetches with the URL's Basic auth: %d, without it: %d; want every one with it", gotAuth.Load(), wrongAuth.Load())
				}
			})
		}
	}
}

// sentRequests is a RoundTripper keeping the requests it is given.
type sentRequests struct{ got []*http.Request }

func (s *sentRequests) RoundTrip(req *http.Request) (*http.Response, error) {
	s.got = append(s.got, req)
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: req}, nil
}

// The hosts and the Authorization of userinfoAuth's test.
const (
	authHost       = "idp.example"
	otherHost      = "other.example"
	ownAuthorizing = "Bearer own"
)

// userinfoAuth adds Basic auth only to a request to its own host, of any
// case, that names no Authorization of its own, and leaves the request it
// is given as it was.
func TestUserinfoAuth(t *testing.T) {
	for name, tc := range map[string]struct {
		host, own string
		wantBasic bool
	}{
		"its host":               {host: authHost, wantBasic: true},
		"its host, another case": {host: strings.ToUpper(authHost), wantBasic: true},
		"another host":           {host: otherHost},
		"its own Authorization":  {host: authHost, own: ownAuthorizing},
	} {
		t.Run(name, func(t *testing.T) {
			sent := &sentRequests{}
			auth := userinfoAuth{next: sent, host: authHost, user: url.UserPassword(urlUser, urlPassword)}
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+tc.host+"/jwks", http.NoBody)
			if err != nil {
				t.Fatal(err)
			}
			if tc.own != "" {
				req.Header.Set("Authorization", tc.own)
			}
			res, err := auth.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = res.Body.Close()
			user, password, ok := sent.got[0].BasicAuth()
			gotBasic := ok && user == urlUser && password == urlPassword
			if gotBasic != tc.wantBasic {
				t.Errorf("Basic auth sent = %v, want %v (Authorization %q)", gotBasic, tc.wantBasic, sent.got[0].Header.Get("Authorization"))
			}
			if tc.own != "" && sent.got[0].Header.Get("Authorization") != tc.own {
				t.Errorf("the request's own Authorization became %q", sent.got[0].Header.Get("Authorization"))
			}
			if tc.own == "" && req.Header.Get("Authorization") != "" {
				t.Error("the request given was changed")
			}
		})
	}
}
