package exchange

import (
	"net/url"
	"strings"
	"testing"
)

// FuzzCheckForm holds the form check to refusing, never panicking on, a body
// a client writes, and to accepting only a token exchange of a subject token
// it reads, with targets RFC 8707 allows and no parameter it forbids.
func FuzzCheckForm(f *testing.F) {
	f.Add(exchangeForm("token").Encode())
	f.Add(exchangeForm("token").Encode() + "&resource=" + url.QueryEscape(toolResource+"#x"))
	f.Add("grant_type=x&grant_type=y")
	f.Fuzz(func(t *testing.T, body string) {
		form, err := url.ParseQuery(body)
		if err != nil || checkForm(form) != nil {
			return
		}
		if form.Get(paramGrantType) != GrantTypeTokenExchange || form.Get(paramSubjectToken) == "" ||
			form.Has(paramActorToken) || form.Has(paramActorTokenType) {
			t.Fatalf("accepted %q", body)
		}
		if typ := form.Get(paramSubjectTokenType); typ != TokenTypeAccessToken && typ != TokenTypeJWT {
			t.Fatalf("accepted subject type %q", typ)
		}
		for name, values := range form {
			if len(values) > 1 && !repeatable[name] {
				t.Fatalf("accepted %s %d times", name, len(values))
			}
		}
		for _, resource := range form[paramResource] {
			if u, err := url.Parse(resource); err != nil || !u.IsAbs() || strings.Contains(resource, "#") {
				t.Fatalf("accepted resource %q", resource)
			}
		}
	})
}
