package limes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"
)

// A name prefix matches as a string, across segments, and never a uri with a
// dot segment.
func TestMatchResourceName(t *testing.T) {
	for _, tc := range []struct {
		prefix, uri string
		want        bool
	}{
		{"drafts/billing-", "drafts/billing-q3", true},
		{"drafts/billing-", "drafts/billing-q3/notes", true},
		{"drafts/billing-", "drafts/billing-", true},
		{"drafts/billing-", "drafts/billing", false},
		{"drafts/billing-", "drafts/other", false},
		{"drafts/billing-", "drafts/billing-q3/../secret", false},
		{"drafts/billing-", "drafts/billing-q3%2f..%2fsecret", false},
		{"", "anything", false},
	} {
		if got := MatchResourceName(tc.prefix, tc.uri); got != tc.want {
			t.Errorf("MatchResourceName(%q, %q) = %v, want %v", tc.prefix, tc.uri, got, tc.want)
		}
	}
	c := Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet}, ResourceNamePrefixes: []string{"drafts/billing-"}}
	if !c.RestrictsResources() || !c.AllowsResource("drafts/billing-q3") || c.AllowsResource("drafts/hr") {
		t.Error("name prefixes do not restrict the caveats to what they name")
	}
}

// What a child may name under a parent, entry by entry: the coverage the
// delegation check applies.
func TestNamePrefixCoverage(t *testing.T) {
	type res struct{ prefixes, uris, names []string }
	caveats := func(r res) Caveats {
		return Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet}, ResourcePrefixes: r.prefixes, ResourceURIs: r.uris, ResourceNamePrefixes: r.names}
	}
	for _, tc := range []struct {
		name          string
		parent, child res
		narrower      bool
	}{
		{"a name equal to a segment prefix", res{prefixes: []string{"a"}}, res{names: []string{"a"}}, false},
		{"a name past a segment prefix's boundary", res{prefixes: []string{"a"}}, res{names: []string{"a/"}}, true},
		{"a name beneath a segment prefix", res{prefixes: []string{"a"}}, res{names: []string{"a/b-"}}, true},
		{"a name equal to a segment prefix ending in /", res{prefixes: []string{"a/"}}, res{names: []string{"a/"}}, true},
		{"a name sharing a segment prefix's text", res{prefixes: []string{"a"}}, res{names: []string{"ab"}}, false},
		{"a name starting with a name", res{names: []string{"a/b-"}}, res{names: []string{"a/b-q"}}, true},
		{"a name shorter than a name", res{names: []string{"a/b-"}}, res{names: []string{"a/b"}}, false},
		{"a segment prefix starting with a name", res{names: []string{"a/b-"}}, res{prefixes: []string{"a/b-q"}}, true},
		{"a segment prefix outside a name", res{names: []string{"a/b-"}}, res{prefixes: []string{"a/b"}}, false},
		{"a URI starting with a name", res{names: []string{"a/b-"}}, res{uris: []string{"a/b-q/x"}}, true},
		{"a URI outside a name", res{names: []string{"a/b-"}}, res{uris: []string{"a/c"}}, false},
		{"a name under URIs alone", res{uris: []string{"a/b"}}, res{names: []string{"a/b"}}, false},
		{"no resource caveat under a name", res{names: []string{"a/b-"}}, res{}, false},
	} {
		err := narrowsResources(caveats(tc.parent), caveats(tc.child))
		if (err == nil) != tc.narrower {
			t.Errorf("%s: narrowsResources = %v, want narrower %v", tc.name, err, tc.narrower)
		}
		if err != nil && !errors.Is(err, ErrDelegationTooWide) {
			t.Errorf("%s: %v, want ErrDelegationTooWide", tc.name, err)
		}
	}
}

// A name prefix is a resource like any other: an empty one, or one with a
// dot segment, is refused; so is one ending inside a percent-escape or a
// UTF-8 sequence, which names no whole character.
func TestValidateNamePrefixes(t *testing.T) {
	for _, bad := range []string{"", "drafts/../hr-", "drafts/./x", "drafts/a%", "drafts/a%2", "drafts/\xc3", "drafts/\xe2\x82"} {
		if err := (Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet}, ResourceNamePrefixes: []string{bad}}).Validate(); !errors.Is(err, ErrInvalidCaveats) {
			t.Errorf("name prefix %q: %v, want ErrInvalidCaveats", bad, err)
		}
	}
}

// Name prefixes travel in the token and read back as they were issued.
func TestNamePrefixesTravelInTheToken(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	req := IssueRequest{
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "a"},
		Audience: []string{audienceData}, TTL: time.Hour, ClientID: testClientID,
		Caveats: Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet}, ResourceNamePrefixes: []string{"drafts/billing-"}},
	}
	_, token, err := issuerOf(t, priv).issue(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	got, err := verifierFor(t, pub, VerifierConfig{}).Verify(t.Context(), VerifyRequest{Token: token, Audience: audienceData})
	if err != nil || !slices.Equal(got.Capability().Caveats.ResourceNamePrefixes, []string{"drafts/billing-"}) {
		t.Fatalf("name prefixes read back as %v, %v", got.Capability().Caveats.ResourceNamePrefixes, err)
	}
}

// A Biscuit block that names resources replaces the name prefixes too, so
// the copy is held to the block's resources alone.
func TestBiscuitBlockReplacesNamePrefixes(t *testing.T) {
	cur := Capability{Caveats: Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet}, ResourceNamePrefixes: []string{"drafts/billing-"}}}
	next := blockFacts{resources: true, prefixes: []string{"drafts/billing-q3"}}.narrow(cur)
	if len(next.Caveats.ResourceNamePrefixes) != 0 || !slices.Equal(next.Caveats.ResourcePrefixes, []string{"drafts/billing-q3"}) {
		t.Errorf("the copy keeps %+v", next.Caveats)
	}
	if err := narrowsResources(cur.Caveats, next.Caveats); err != nil {
		t.Errorf("a block within the name prefix: %v", err)
	}
}

// fuzzAlphabet is what the fuzzer builds resource names from: segments,
// separators, a dot and an escape, so the shapes that matter turn up.
const fuzzAlphabet = "ab/-.%2f"

// resourcesFrom builds up to fuzzEntries resource entries from data.
func resourcesFrom(data string) []string {
	const fuzzEntries = 3
	var out []string
	for part := range strings.SplitSeq(data, ",") {
		if len(out) == fuzzEntries {
			break
		}
		var b strings.Builder
		for _, r := range part {
			b.WriteByte(fuzzAlphabet[int(r)%len(fuzzAlphabet)])
		}
		if b.Len() > 0 {
			out = append(out, b.String())
		}
	}
	return out
}

// FuzzNarrowingNeverWidens holds delegation to its promise: whatever
// resource caveats a parent and child have, if the child passes as narrower
// then every resource the child allows, the parent allows too.
func FuzzNarrowingNeverWidens(f *testing.F) {
	f.Add("a", "", "", "", "", "a/", "a/b")
	f.Add("", "", "a/b-", "a/b", "", "", "a/bc")
	f.Add("a", "", "", "", "", "a", "ab")
	f.Add("", "a/b", "", "", "", "a/b", "a/b/c")
	f.Fuzz(func(t *testing.T, pp, pu, pn, cp, cu, cn, uri string) {
		parent := Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet}, ResourcePrefixes: resourcesFrom(pp), ResourceURIs: resourcesFrom(pu), ResourceNamePrefixes: resourcesFrom(pn)}
		child := Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet}, ResourcePrefixes: resourcesFrom(cp), ResourceURIs: resourcesFrom(cu), ResourceNamePrefixes: resourcesFrom(cn)}
		if narrowsResources(parent, child) != nil {
			return
		}
		candidates := append([]string{uri}, child.ResourcePrefixes...)
		candidates = append(candidates, child.ResourceURIs...)
		candidates = append(candidates, child.ResourceNamePrefixes...)
		for _, base := range slices.Clone(candidates) {
			for _, suffix := range []string{"", "/x", "x", "-x", "/..", "%2f..", "/x/y"} {
				candidates = append(candidates, base+suffix)
			}
		}
		for _, u := range candidates {
			if child.AllowsResource(u) && !parent.AllowsResource(u) {
				t.Fatalf("child %+v allows %q, which parent %+v does not", child, u, parent)
			}
		}
	})
}

// A name prefix ending on whole characters, escaped or not, is accepted.
func TestNamePrefixesOfWholeCharacters(t *testing.T) {
	for _, good := range []string{"drafts/a%2F", "drafts/caf\u00e9-", "drafts/100%25-", "drafts/a"} {
		if err := (Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet}, ResourceNamePrefixes: []string{good}}).Validate(); err != nil {
			t.Errorf("name prefix %q: %v", good, err)
		}
	}
}
