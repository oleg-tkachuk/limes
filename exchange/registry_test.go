package exchange

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"
	"testing"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/dpop"
	"github.com/oleg-tkachuk/limes/internal/errcode"
	"github.com/oleg-tkachuk/limes/jwks"
)

// errorsDoc is limes's reference of every code, the exchange's among them.
const errorsDoc = "../docs/errors.md"

// docAnchor is an anchor of errorsDoc, as a problem type's fragment names it.
var docAnchor = regexp.MustCompile(`<a id="([^"]+)"></a>`)

// Every sentinel of the exchange is registered with limes, or wraps one that
// is, and each registered one carries the answer the endpoint gives it.
func TestEverySentinelIsRegistered(t *testing.T) {
	var registered []error
	for _, r := range errcode.Registered() {
		if r.Component == errcode.ComponentExchange {
			registered = append(registered, r.Err)
		}
	}
	for name, err := range exchangeSentinels {
		if !slices.ContainsFunc(registered, func(r error) bool { return errors.Is(err, r) }) {
			t.Errorf("%s is registered with limes neither itself nor through a sentinel it wraps", name)
		}
	}
	for _, err := range registered {
		answered, ok := err.(errcode.Answered) //nolint:errorlint // a registration is the sentinel itself
		if !ok {
			t.Fatalf("%v registered without its answer", err)
		}
		want, _ := ErrorOf(err)
		if answered.LimesErrorStatus() != want.Status || answered.LimesErrorCode() != want.Code || answered.LimesErrorTitle() == "" ||
			!slices.Contains(limes.Categories(), limes.Category(answered.LimesErrorCategory())) {
			t.Errorf("%v carries %d %q %q %q; the endpoint answers %d %q, and a title and one of limes.Categories() are due",
				err, answered.LimesErrorStatus(), answered.LimesErrorCode(), answered.LimesErrorTitle(), answered.LimesErrorCategory(), want.Status, want.Code)
		}
	}
}

// Every problem type the exchange's codes give is an anchor of errorsDoc,
// once, as limes's own are: its test reaches no code of the exchange's.
func TestEveryExchangeProblemTypeIsAnAnchorOfTheErrorsDoc(t *testing.T) {
	src, err := os.ReadFile(errorsDoc)
	if err != nil {
		t.Fatal(err)
	}
	anchors := map[string]int{}
	for _, m := range docAnchor.FindAllStringSubmatch(string(src), -1) {
		anchors[m[1]]++
	}
	types := map[string]bool{}
	for _, code := range ErrorCodes() {
		types[code] = true
	}
	for _, err := range codedSentinels {
		p, ok := limes.ProblemOf(err)
		if !ok || p.Type != limes.ProblemTypeURI(string(p.Code)) {
			t.Errorf("limes.ProblemOf(%v) = %+v, %v; want a type naming its code", err, p, ok)
		}
		types[string(p.Code)] = true
	}
	for typ := range types {
		if anchors[typ] != 1 {
			t.Errorf("%s has %d anchors %q, want 1: add <a id=%q></a> to its row", errorsDoc, anchors[typ], typ, typ)
		}
	}
}

// A category spans one class of status across every code limes answers,
// the exchange's among them: the client's 4xx or the server's 5xx.
func TestEveryCategoryIsOneStatusClass(t *testing.T) {
	classes := map[limes.Category]map[int][]limes.Code{}
	for _, code := range limes.Codes() {
		p, ok := limes.ProblemOfCode(code)
		if !ok {
			t.Fatalf("limes.ProblemOfCode(%q) answers nothing", code)
		}
		class := p.Status / http.StatusContinue
		if classes[p.Category] == nil {
			classes[p.Category] = map[int][]limes.Code{}
		}
		classes[p.Category][class] = append(classes[p.Category][class], code)
	}
	for category, byClass := range classes {
		if len(byClass) != 1 {
			t.Errorf("category %q spans status classes %v", category, byClass)
		}
	}
}

// keysOutOfReach verifies no subject token: its identity provider's keys
// are out of reach.
type keysOutOfReach struct{}

func (keysOutOfReach) VerifySubject(context.Context, string) (Subject, error) {
	return Subject{}, fmt.Errorf("idp: %w", jwks.ErrUnavailable)
}

// Every error an Exchanger returns is answered by limes.ProblemOf with the
// status the token endpoint answers it with — a failure of the server's,
// whatever limes error it wraps, 500 at both — but for a refused proof,
// which RFC 9449 answers 400 at a token endpoint and 401 at a resource:
// there the class of status agrees, the client's.
func TestTheEndpointAndProblemOfAgree(t *testing.T) {
	refuse := func(err error) Policy {
		return func(context.Context, Grant) (limes.IssueRequest, error) { return limes.IssueRequest{}, err }
	}
	for name, tc := range map[string]struct {
		change func(*fixture)
		token  func(*fixture) string
		proof  string
		// classOnly compares the class of status alone.
		classOnly bool
		// cause is an error the Exchanger's must wrap, nil for none.
		cause error
	}{
		"a subject token refused": {token: func(*fixture) string { return "not-a-token" }},
		"a policy refusing":       {change: func(f *fixture) { f.ex.cfg.Policy = refuse(ErrInvalidScope) }},
		"a policy failing":        {change: func(f *fixture) { f.ex.cfg.Policy = refuse(errors.New("policy store down")) }},
		"a policy failing as a limes error": {
			change: func(f *fixture) { f.ex.cfg.Policy = refuse(fmt.Errorf("policy: %w", limes.ErrNotFound)) },
			cause:  limes.ErrNotFound,
		},
		"an issue refused": {
			change: func(f *fixture) {
				f.ex.cfg.Policy = func(ctx context.Context, g Grant) (limes.IssueRequest, error) {
					issue, err := f.readPolicy(ctx, g)
					issue.Caveats.Requests = limes.RequestCeiling{}
					return issue, err
				}
			},
			cause: limes.ErrInvalidCaveats,
		},
		"the policy reaching past the request": {
			change: func(f *fixture) {
				f.ex.cfg.Policy = func(ctx context.Context, g Grant) (limes.IssueRequest, error) {
					issue, err := f.readPolicy(ctx, g)
					issue.Audience = []string{"https://elsewhere.example.com"}
					return issue, err
				}
			},
			cause: errPolicy,
		},
		"the identity provider's keys out of reach": {change: func(f *fixture) { f.ex.cfg.Subjects = keysOutOfReach{} }, cause: jwks.ErrUnavailable},
		"a proof invalid": {proof: "not-a-proof", classOnly: true, cause: dpop.ErrInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, fixtureConfig{})
			if tc.change != nil {
				tc.change(f)
			}
			token := f.idp.sign(t, typeAccessToken, accessClaims())
			if tc.token != nil {
				token = tc.token(f)
			}
			req := request(token)
			header := http.Header{}
			if tc.proof != "" {
				req.DPoP = dpop.Proof{JWT: tc.proof, Method: http.MethodPost, URL: tokenEndpoint}
				header.Set(headerDPoP, tc.proof)
			}
			_, err := f.ex.Exchange(t.Context(), req)
			if err == nil || (tc.cause != nil && !errors.Is(err, tc.cause)) {
				t.Fatalf("Exchange = %v; want an error wrapping %v", err, tc.cause)
			}
			endpoint := post(t, newHandler(t, f), exchangeForm(token), header).Code
			got := limes.ProblemFor(err).Status
			if tc.classOnly {
				endpoint, got = endpoint/http.StatusContinue, got/http.StatusContinue
			}
			if got != endpoint {
				t.Errorf("limes.ProblemFor(%v) answers %d; the endpoint %d", err, got, endpoint)
			}
		})
	}
}

// A failure of the server's keeps its cause, and is answered server_error
// in process as at the endpoint, though it wraps a limes error the client
// could be blamed for.
func TestAServerFailureIsTheServers(t *testing.T) {
	for _, cause := range []error{limes.ErrInvalidCaveats, limes.ErrNotFound, jwks.ErrUnavailable, errors.New("store down")} {
		err := serverError(fmt.Errorf("exchange: issue: %w", cause))
		if !errors.Is(err, ErrServerError) || !errors.Is(err, cause) || isRefusal(err) {
			t.Errorf("serverError(%v) = %v; want an ErrServerError wrapping it", cause, err)
		}
		if p, ok := limes.ProblemOf(err); !ok || p.Code != limes.CodeServerError || p.Status != http.StatusInternalServerError || p.Category != limes.CategoryInternal {
			t.Errorf("limes.ProblemOf(%v) = %+v, %v; want server_error, 500, internal", err, p, ok)
		}
	}
	for _, err := range []error{nil, ErrInvalidGrant, fmt.Errorf("x: %w", dpop.ErrInvalid), errPolicy} {
		if got := serverError(err); !errors.Is(got, err) || got != err { //nolint:errorlint // the very value, unwrapped
			t.Errorf("serverError(%v) = %v; want it as it is", err, got)
		}
	}
}
