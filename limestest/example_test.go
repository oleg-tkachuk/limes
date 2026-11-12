package limestest_test

import (
	"net/http"
	"testing"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/limestest"
)

// The checks take the consumer's own *testing.T, so the examples show the
// test function rather than running it.

// A consumer that answers in HTTP's terms pins each code's status and
// category; a limes that answers one otherwise fails this test.
func ExampleCheckCodes() {
	_ = func(t *testing.T) {
		limestest.CheckCodes(t, map[limes.Code]limestest.Answer{
			limes.CodeRevoked:        {Status: http.StatusUnauthorized, Category: limes.CategoryUnauthenticated},
			limes.CodeBudgetExceeded: {Status: http.StatusTooManyRequests, Category: limes.CategoryExhausted},
			// … every other code the consumer meets.
		}, limestest.WithoutExchange())
	}
}

// grpcCode stands for a consumer transport's own status type.
type grpcCode int

// The codes of the consumer's transport its mapping answers with.
const (
	grpcUnauthenticated grpcCode = iota + 1
	grpcResourceExhausted
	grpcInternal
)

// A consumer with a transport of its own pins what its own mapping answers
// each code with. AcceptNewByCategory passes a code limes adds to a
// category the consumer maps whole, and logs it for pinning.
func ExampleCheckMapping() {
	toGRPC := func(c limes.Code) grpcCode {
		switch category, _ := limes.CategoryOfCode(c); category {
		case limes.CategoryUnauthenticated:
			return grpcUnauthenticated
		case limes.CategoryExhausted:
			return grpcResourceExhausted
		default:
			return grpcInternal
		}
	}
	_ = func(t *testing.T) {
		limestest.CheckMapping(t, map[limes.Code]grpcCode{
			limes.CodeRevoked:        grpcUnauthenticated,
			limes.CodeBudgetExceeded: grpcResourceExhausted,
			// … every other code the consumer meets.
		}, toGRPC, limestest.WithoutExchange(), limestest.AcceptNewByCategory())
	}
}

// A consumer mapping categories onto its transport proves it maps every one
// limes has: a category added later fails the test until it is mapped.
func ExampleCheckCategories() {
	mapped := map[limes.Category]bool{
		limes.CategoryUnauthenticated: true, limes.CategoryExhausted: true,
		// … every other category.
	}
	_ = func(t *testing.T) {
		limestest.CheckCategories(t, func(c limes.Category) bool { return mapped[c] })
	}
}
