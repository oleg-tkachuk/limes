package limes

import (
	"context"
	"crypto/ed25519"
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
		// A dot segment names a place only a resolver can tell: never a match.
		{"corpus/public", "corpus/public/../private/secret", false},
		{"corpus/public", "corpus/public/./x", false},
		{"corpus/public", "corpus/public/%2e%2E/private", false},
		{"corpus/public", "corpus/public/..", false},
		{"corpus/public", "corpus/public/..x", true},
		// Nor between slashes a consumer that decodes first would split on.
		{"corpus/public", "corpus/public/..%2Fsecret", false},
		{"corpus/public", "corpus/public/..%2fsecret", false},
		{"corpus/public", `corpus/public/..\secret`, false},
		{"corpus/public", "corpus/public/..%5Csecret", false},
		{"corpus/public", "corpus/public/%2e%2e%2Fsecret", false},
		{"corpus/public", "corpus/public/a%2Fb", true},
		// Nor encoded twice, for a consumer that decodes twice.
		{"corpus/public", "corpus/public/%252e%252e/private", false},
		{"corpus/public", "corpus/public/%252E%252e/private", false},
		{"corpus/public", "corpus/public/..%252fsecret", false},
		{"corpus/public", "corpus/public/..%255Csecret", false},
		{"corpus/public", "corpus/public/100%25.txt", true},
		// Nor encoded twice with the second escape's own hex digits encoded.
		{"corpus/public", "corpus/public/%25%32%65%25%32%65/private", false},
		{"corpus/public", "corpus/public/%25%32e%25%32E/private", false},
		{"corpus/public", "corpus/public/%252%65%252%65/private", false},
		{"corpus/public", "corpus/public/..%25%32%66private", false},
		{"corpus/public", "corpus/public/..%25%35%63private", false},
		// A stray "%" decodes as itself and stops nothing.
		{"corpus/public", "corpus/public/50%/..", false},
		{"corpus/public", "corpus/public/50%x", true},
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
	p := Capability{Audience: []string{"data"}, ExpiresAt: exp, Subject: Principal{Type: PrincipalTypeAgent, TenantID: tenant}, Caveats: parent}
	c := Capability{Audience: []string{"data"}, ExpiresAt: exp, Subject: Principal{Type: PrincipalTypeAgent, TenantID: tenant}, Caveats: child}
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

// A child starting before its parent is valid at a moment the parent is not:
// Narrows compares the whole window, its start as well as its end.
func TestNarrowsRefusesAnEarlierStart(t *testing.T) {
	start := time.Now().Add(time.Hour)
	cases := []struct {
		name        string
		parentStart time.Time
		childStart  time.Time
		ok          bool
	}{
		{"child with no start under a parent that starts later", start, time.Time{}, false},
		{"child issued after its parent started", start.Add(-time.Hour), time.Time{}, true},
		{"child starting before its parent", start, start.Add(-time.Minute), false},
		{"child starting with its parent", start, start, true},
		{"child starting after its parent", start, start.Add(time.Minute), true},
		{"any child of a parent with no start", time.Time{}, time.Time{}, true},
	}
	for _, tc := range cases {
		p, c := narrowFixture(Caveats{Ops: []Op{OpGet}}, Caveats{Ops: []Op{OpGet}})
		p.ExpiresAt = start.Add(2 * time.Hour)
		c.ExpiresAt = p.ExpiresAt
		p.NotBefore, c.NotBefore = tc.parentStart, tc.childStart
		if tc.childStart.IsZero() {
			c.IssuedAt = start.Add(-time.Minute) // issued before start, after start - 1h
		}
		err := Narrows(p, c)
		if (err == nil) != tc.ok {
			t.Errorf("%s: Narrows = %v, want ok=%v", tc.name, err, tc.ok)
		}
		if err != nil && !errors.Is(err, ErrDelegationTooWide) {
			t.Errorf("%s: err %v does not match ErrDelegationTooWide", tc.name, err)
		}
	}
}

// Delegate starts a child no earlier than its parent, as it ends one no later.
func TestDelegateStartsNoEarlierThanItsParent(t *testing.T) {
	now := goldenClock()
	iss := testIssuer(t, goldenKID, ed25519.NewKeyFromSeed(goldenSeed[:]), func() time.Time { return now })
	parent, _, err := iss.issue(context.Background(), IssueRequest{
		IssuedBy: Principal{Subject: "test-operator"},
		Subject:  Principal{Type: PrincipalTypeService, TenantID: uuid.MustParse(goldenTenant), Subject: "svc"},
		Audience: []string{goldenAudience}, Caveats: Caveats{Ops: []Op{OpGet, OpShare}},
		NotBefore: now.Add(time.Minute), TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	child, _, err := iss.delegate(context.Background(), DelegateRequest{Subject: Principal{Type: PrincipalTypeAgent, Subject: "child"}, Parent: asVerified(parent), InheritCaveats: true})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	if child.NotBefore.Before(parent.NotBefore) {
		t.Errorf("child starts %s, before its parent at %s", child.NotBefore, parent.NotBefore)
	}

	// A parent from an issuer replica whose clock runs ahead: issued, with no
	// not-before, a few seconds after this replica's now.
	const skew = 5 * time.Second
	ahead := parent
	ahead.NotBefore, ahead.IssuedAt = time.Time{}, now.Add(skew)
	child, _, err = iss.delegate(context.Background(), DelegateRequest{Subject: Principal{Type: PrincipalTypeAgent, Subject: "child"}, Parent: asVerified(ahead), InheritCaveats: true})
	if err != nil {
		t.Fatalf("Delegate from a parent issued %s ahead: %v", skew, err)
	}
	if child.NotBefore.Before(ahead.IssuedAt) {
		t.Errorf("child starts %s, before its parent was issued at %s", child.NotBefore, ahead.IssuedAt)
	}
}

// FuzzNarrowsKeepsTheWindow checks the same property for time: whatever
// Narrows accepts, the child is valid at no moment the parent is not.
func FuzzNarrowsKeepsTheWindow(f *testing.F) {
	f.Add(int8(0), int8(10), int8(1), int8(9))
	f.Add(int8(5), int8(10), int8(0), int8(10))
	f.Add(int8(-1), int8(3), int8(-1), int8(4))
	f.Fuzz(func(t *testing.T, ps, pe, cs, ce int8) {
		base := time.Unix(1_700_000_000, 0).UTC()
		at := func(n int8) time.Time {
			if n < 0 {
				return time.Time{} // no start
			}
			return base.Add(time.Duration(n) * time.Minute)
		}
		p, c := narrowFixture(Caveats{Ops: []Op{OpGet}}, Caveats{Ops: []Op{OpGet}})
		p.NotBefore, p.ExpiresAt = at(ps), base.Add(time.Duration(pe)*time.Minute)
		c.NotBefore, c.ExpiresAt = at(cs), base.Add(time.Duration(ce)*time.Minute)
		if Narrows(p, c) != nil {
			return
		}
		if c.ExpiresAt.After(p.ExpiresAt) {
			t.Fatalf("child ends %s, after parent %s", c.ExpiresAt, p.ExpiresAt)
		}
		if !p.NotBefore.IsZero() && (c.NotBefore.IsZero() || c.NotBefore.Before(p.NotBefore)) {
			t.Fatalf("child starts %s, before parent %s", c.NotBefore, p.NotBefore)
		}
	})
}

// A request naming a resource through a dot segment is refused by a
// restricted capability, prefix or exact URI alike.
func TestAllowsResourceRefusesDotSegments(t *testing.T) {
	c := Caveats{Ops: []Op{OpGet}, ResourcePrefixes: []string{"corpus/public"}, ResourceURIs: []string{"exact/one"}}
	for _, uri := range []string{"corpus/public/../private/x", "exact/one/..", "corpus/public/./a"} {
		if c.AllowsResource(uri) {
			t.Errorf("AllowsResource(%q) = true", uri)
		}
		if err := c.Check(CheckRequest{Op: OpGet, Resource: uri}); !errors.Is(err, ErrResourceNotAllowed) {
			t.Errorf("Check(%q) = %v, want ErrResourceNotAllowed", uri, err)
		}
	}
}
