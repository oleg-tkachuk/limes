package limes

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ─── resource matching and narrowing ───────────────────────────────────────

func TestMatchResourceRespectsSegmentBoundaries(t *testing.T) {
	cases := []struct {
		prefix, uri string
		want        bool
	}{
		{"corpus/public", "corpus/public", true},
		{"corpus/public", "corpus/public/a.txt", true},
		{"corpus/public", "corpus/public-secret/a.txt", false},
		{"corpus/public/", "corpus/public/a.txt", true},
		{"corpus/public/", "corpus/public", false},
		{"object://t/c/", "object://t/c/k", true},
		{"", "anything", false},
	}
	for _, tc := range cases {
		if got := MatchResource(tc.prefix, tc.uri); got != tc.want {
			t.Errorf("MatchResource(%q, %q) = %v, want %v", tc.prefix, tc.uri, got, tc.want)
		}
	}
}

func narrowFixture(parent, child Caveats) (Capability, Capability) {
	tenant := uuid.New()
	exp := time.Now().Add(time.Hour)
	p := Capability{Audience: []string{"data"}, ExpiresAt: exp, Subject: Principal{TenantID: tenant}, Caveats: parent}
	c := Capability{Audience: []string{"data"}, ExpiresAt: exp, Subject: Principal{TenantID: tenant}, Caveats: child}
	return p, c
}

// The escalation that motivated the rewrite: a parent pinned to exact URIs
// used to admit any child prefix, because child prefixes were only compared
// when the parent itself had prefixes.
func TestNarrowsRefusesPrefixChildOfURIParent(t *testing.T) {
	p, c := narrowFixture(
		Caveats{Ops: []Op{OpGet}, ResourceURIs: []string{"a/report.pdf"}},
		Caveats{Ops: []Op{OpGet}, ResourceURIs: []string{"a/report.pdf"}, ResourcePrefixes: []string{"secret/"}},
	)
	if err := Narrows(p, c); !errors.Is(err, ErrDelegationTooWide) {
		t.Fatalf("Narrows = %v, want ErrDelegationTooWide", err)
	}
}

func TestNarrowsResourceCases(t *testing.T) {
	cases := []struct {
		name          string
		parent, child Caveats
		ok            bool
	}{
		{"prefix under prefix", Caveats{ResourcePrefixes: []string{"a/"}}, Caveats{ResourcePrefixes: []string{"a/b/"}}, true},
		{"sibling across boundary", Caveats{ResourcePrefixes: []string{"a/b"}}, Caveats{ResourcePrefixes: []string{"a/bc"}}, false},
		{"uri under prefix", Caveats{ResourcePrefixes: []string{"a/"}}, Caveats{ResourceURIs: []string{"a/x"}}, true},
		{"uri child of prefix parent may omit prefixes", Caveats{ResourcePrefixes: []string{"a/"}}, Caveats{ResourceURIs: []string{"a/x"}}, true},
		{"uri not in uri parent", Caveats{ResourceURIs: []string{"a/x"}}, Caveats{ResourceURIs: []string{"a/y"}}, false},
		{"unrestricted child of restricted parent", Caveats{ResourceURIs: []string{"a/x"}}, Caveats{}, false},
		{"anything under unrestricted parent", Caveats{}, Caveats{ResourcePrefixes: []string{"z/"}}, true},
	}
	for _, tc := range cases {
		tc.parent.Ops, tc.child.Ops = []Op{OpGet}, []Op{OpGet}
		p, c := narrowFixture(tc.parent, tc.child)
		err := Narrows(p, c)
		if (err == nil) != tc.ok {
			t.Errorf("%s: Narrows = %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}

func TestNarrowsSourceIPUsesContainment(t *testing.T) {
	cases := []struct {
		parent, child []string
		ok            bool
	}{
		{[]string{"10.0.0.0/8"}, []string{"10.1.0.0/16"}, true},
		{[]string{"10.0.0.0/8"}, []string{"10.0.0.0/8"}, true},
		{[]string{"10.1.0.0/16"}, []string{"10.0.0.0/8"}, false},
		{[]string{"10.0.0.0/8"}, []string{"192.168.0.0/24"}, false},
		{[]string{"10.0.0.0/8"}, nil, false},
		{[]string{"10.0.0.0/8"}, []string{"::/0"}, false},
	}
	for _, tc := range cases {
		p, c := narrowFixture(
			Caveats{Ops: []Op{OpGet}, SourceIPCIDR: tc.parent},
			Caveats{Ops: []Op{OpGet}, SourceIPCIDR: tc.child},
		)
		err := Narrows(p, c)
		if (err == nil) != tc.ok {
			t.Errorf("parent %v child %v: Narrows = %v, want ok=%v", tc.parent, tc.child, err, tc.ok)
		}
		if err != nil && !errors.Is(err, ErrDelegationTooWide) {
			t.Errorf("parent %v child %v: err %v does not match ErrDelegationTooWide", tc.parent, tc.child, err)
		}
	}
}

// fuzzResources draws resource strings from a tiny alphabet so parents and
// children overlap often enough for the property to bite.
func fuzzResources(b []byte, n int) []string {
	parts := []string{"a", "a/", "a/b", "a/b/", "ab", "a/bc", "b/", "a/b/c"}
	var out []string
	for i := 0; i < n && i < len(b); i++ {
		if b[i]%3 == 0 {
			continue
		}
		out = append(out, parts[int(b[i])%len(parts)])
	}
	return out
}

// FuzzNarrowsNeverWidens checks the property delegation exists to provide:
// whatever Narrows accepts, the child can reach no resource and perform no
// operation the parent could not.
func FuzzNarrowsNeverWidens(f *testing.F) {
	f.Add([]byte{1, 2, 4}, []byte{5, 7, 8}, []byte{1, 2}, []byte{4})
	f.Add([]byte{0, 0, 0}, []byte{1, 1, 1}, []byte{2, 2, 2}, []byte{3})
	f.Add([]byte{7, 1}, []byte{}, []byte{2, 5}, []byte{8, 1})
	f.Fuzz(func(t *testing.T, pp, pu, cp, cu []byte) {
		parent := Caveats{Ops: []Op{OpGet, OpList}, ResourcePrefixes: fuzzResources(pp, 3), ResourceURIs: fuzzResources(pu, 3)}
		child := Caveats{Ops: []Op{OpGet}, ResourcePrefixes: fuzzResources(cp, 3), ResourceURIs: fuzzResources(cu, 3)}
		p, c := narrowFixture(parent, child)
		if Narrows(p, c) != nil {
			return
		}
		probes := []string{"a", "a/", "a/b", "a/b/", "a/b/c", "a/b/c/d", "ab", "a/bc", "a/bc/d", "b/", "b/x", "z"}
		for _, uri := range probes {
			if child.AllowsResource(uri) && !parent.AllowsResource(uri) {
				t.Fatalf("child %+v reaches %q, parent %+v does not", child, uri, parent)
			}
		}
	})
}
