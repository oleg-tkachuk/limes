package jwks

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// password is the secret a key set URL carries in its userinfo.
const password = "s3cret-in-the-url"

// A password in the key set's URL is the operator's secret: no error names
// it, whether NewRemote refuses the URL or a fetch from it fails, since an
// error is what reaches a log.
func TestRemoteErrorsRedactTheURLsPassword(t *testing.T) {
	for name, u := range map[string]string{
		"a URL that does not parse": "https://user:" + password + "@issuer.example/%zz",
		"a URL that is not https":   "http://user:" + password + "@issuer.example/jwks",
	} {
		_, err := NewRemote(RemoteConfig{URL: u})
		if err == nil {
			t.Errorf("%s: NewRemote accepted it", name)
			continue
		}
		if strings.Contains(err.Error(), password) {
			t.Errorf("%s: NewRemote = %q, names the password", name, err)
		}
	}

	failing := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(failing.Close)
	u := strings.Replace(failing.URL, "https://", "https://user:"+password+"@", 1)
	r, err := NewRemote(RemoteConfig{URL: u, Client: failing.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.PublicKey(context.Background(), "any")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("PublicKey = %v, want ErrUnavailable", err)
	}
	if strings.Contains(err.Error(), password) {
		t.Errorf("PublicKey = %q, names the password", err)
	}
	if !strings.Contains(err.Error(), failing.Listener.Addr().String()) {
		t.Errorf("PublicKey = %q, does not name the endpoint", err)
	}
}
