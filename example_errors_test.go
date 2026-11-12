package limes_test

import (
	"errors"
	"fmt"

	"github.com/oleg-tkachuk/limes"
)

func ExampleCodeOf() {
	// A refusal keeps its code through any wrapping.
	code, ok := limes.CodeOf(fmt.Errorf("charge: %w", limes.ErrBudgetExceeded))
	fmt.Println(code, ok)

	// An error that is not limes's has none: answer it as your own failure.
	_, ok = limes.CodeOf(errors.New("database unreachable"))
	fmt.Println(ok)
	// Output:
	// budget_exceeded true
	// false
}

func ExampleProblemOf() {
	// The RFC 9457 body for a consumer's own API, whatever its transport.
	// The title is fixed per answer and names nothing of the request.
	p, ok := limes.ProblemOf(fmt.Errorf("verify: %w", limes.ErrRevoked))
	fmt.Println(ok, p.Status, p.Code, p.Category, p.Reason)
	fmt.Println(p.Title)

	_, ok = limes.ProblemOf(errors.New("database unreachable"))
	fmt.Println(ok)
	// Output:
	// true 401 revoked unauthenticated revoked
	// The access token is invalid
	// false
}

func ExampleProblemFor() {
	// Every error gets a body: one that is not limes's is the server's.
	p := limes.ProblemFor(errors.New("database unreachable"))
	fmt.Println(p.Status, p.Code, p.Category)
	// Output:
	// 500 internal internal
}

func ExampleCategoryOf() {
	// A consumer maps the few categories, not every code, onto its transport.
	category, ok := limes.CategoryOf(fmt.Errorf("authorize: %w", limes.ErrOpNotAllowed))
	fmt.Println(category, ok)
	// Output:
	// denied true
}
