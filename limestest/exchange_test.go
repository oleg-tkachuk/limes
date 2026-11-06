package limestest_test

import (
	"net/http"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/errcode"
)

// exchangeError stands in for a sentinel of module exchange, which this
// module cannot import: it carries its answer, as the exchange's do.
type exchangeError struct {
	code     limes.Code
	status   int
	category limes.Category
}

func (e exchangeError) Error() string              { return string(e.code) }
func (e exchangeError) LimesErrorCode() string     { return string(e.code) }
func (e exchangeError) LimesErrorStatus() int      { return e.status }
func (e exchangeError) LimesErrorTitle() string    { return string(e.code) }
func (e exchangeError) LimesErrorCategory() string { return string(e.category) }

// The exchange registers its codes as it initialises, in a binary that
// links it in; this test binary stands them in, as docs/errors.md lists
// them.
func init() {
	errcode.Register(errcode.ComponentExchange,
		exchangeError{limes.CodeInvalidClient, http.StatusUnauthorized, limes.CategoryUnauthenticated},
		exchangeError{limes.CodeUnauthorizedClient, http.StatusBadRequest, limes.CategoryDenied},
		exchangeError{limes.CodeUnsupportedGrantType, http.StatusBadRequest, limes.CategoryInvalid},
		exchangeError{limes.CodeInvalidGrant, http.StatusBadRequest, limes.CategoryUnauthenticated},
		exchangeError{limes.CodeInvalidTarget, http.StatusBadRequest, limes.CategoryDenied},
		exchangeError{limes.CodeInvalidScope, http.StatusBadRequest, limes.CategoryDenied},
		exchangeError{limes.CodeInvalidAuthorizationDetails, http.StatusBadRequest, limes.CategoryDenied},
		exchangeError{limes.CodeServerError, http.StatusInternalServerError, limes.CategoryInternal},
	)
}
