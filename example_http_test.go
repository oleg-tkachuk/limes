package limes_test

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"

	"github.com/oleg-tkachuk/limes"
)

func ExampleParseAuthorization() {
	scheme, token, err := limes.ParseAuthorization("dpop eyJhbGciOi")
	fmt.Println(scheme, token, err)

	// A scheme limes does not read is no credential at all.
	_, _, err = limes.ParseAuthorization("Basic dXNlcjpwYXNz")
	fmt.Println(errors.Is(err, limes.ErrNoCredentials))

	// Bearer with two credentials is malformed; the scheme still comes back,
	// so the challenge answers under it.
	scheme, _, err = limes.ParseAuthorization("Bearer a b")
	fmt.Println(scheme, errors.Is(err, limes.ErrMalformedAuthorization))
	// Output:
	// DPoP eyJhbGciOi <nil>
	// true
	// Bearer true
}

func ExampleVerifier_ErrorResponse() {
	ctx := context.Background()
	s := newExampleStack()
	_, err := s.verifier.Verify(ctx, limes.VerifyRequest{Token: "not-a-token", Audience: exampleAudience})

	resp := s.verifier.ErrorResponse(ctx, err, limes.AuthSchemeBearer)
	fmt.Println(resp.Status, resp.Problem.Reason)
	fmt.Println(resp.Challenges[0])
	// Output:
	// 401 invalid_token
	// Bearer error="invalid_token", error_description="The access token is invalid", realm="limes"
}

func ExampleWriteErrorResponse() {
	ctx := context.Background()
	s := newExampleStack()
	_, token := s.issue()
	_, err := s.verifier.Authorize(ctx, limes.AuthorizeRequest{
		Verify: limes.VerifyRequest{Token: token, Audience: exampleAudience},
		Check:  limes.CheckRequest{Op: limes.OpDelete, Resource: "corpus/public/a.txt"},
	})

	rec := httptest.NewRecorder()
	limes.WriteErrorResponse(rec, s.verifier.ErrorResponse(ctx, err, limes.AuthSchemeBearer))
	fmt.Println(rec.Code, rec.Header().Get("Content-Type"))
	fmt.Print(rec.Body.String())
	// Output:
	// 403 application/problem+json
	// {"type":"https://github.com/oleg-tkachuk/limes/problems/op_not_allowed","title":"The token does not allow this request","status":403,"reason":"op_not_allowed"}
}

func ExampleReasonOf() {
	reason, ok := limes.ReasonOf(fmt.Errorf("charge: %w", limes.ErrBudgetExceeded))
	fmt.Println(reason, ok, reason.Permanent())

	reason, ok = limes.ReasonOf(limes.ErrRevoked)
	fmt.Println(reason, ok, reason.Permanent())

	// A programming error is not a refusal of the capability.
	_, ok = limes.ReasonOf(limes.ErrInvalidRequest)
	fmt.Println(ok)
	// Output:
	// budget_exceeded true false
	// revoked true true
	// false
}

func ExampleReasons() {
	// A consumer proves its own wire mapping covers every reason.
	mapped := map[limes.Reason]int{limes.ReasonRevoked: 401}
	missing := 0
	for _, r := range limes.Reasons() {
		if _, ok := mapped[r]; !ok {
			missing++
		}
	}
	fmt.Println(len(limes.Reasons()), missing)
	// Output:
	// 22 21
}

func ExampleVerifier_DPoPNonce() {
	// A verifier configured with no dpop.Nonces hands out none.
	s := newExampleStack()
	nonce, err := s.verifier.DPoPNonce(context.Background())
	fmt.Printf("%q %v\n", nonce, err)
	// Output:
	// "" <nil>
}
