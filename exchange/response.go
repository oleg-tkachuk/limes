package exchange

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/oleg-tkachuk/limes/dpop"
)

// MaxErrorResponseBytes bounds the body ParseErrorResponse reads: an error
// response is a code and a sentence, and a server that sends more is not
// answering as RFC 6749 §5.2 says.
const MaxErrorResponseBytes = 16 << 10

// responseSentinels is the sentinel each code a token endpoint answers is
// read as: the one ErrorOf answers with that code, and for a code several
// sentinels share, the one the others are cases of — every DPoP proof
// refused (dpop.ErrInvalid), every target refused (ErrInvalidTarget).
var responseSentinels = map[string]error{
	codeUseDPoPNonce:                dpop.ErrNonceRequired,
	codeInvalidDPoPProof:            dpop.ErrInvalid,
	codeTemporarilyUnavailable:      dpop.ErrReplayQuota,
	codeUnauthorizedClient:          ErrUnauthorizedClient,
	codeInvalidClient:               ErrInvalidClient,
	codeUnsupportedGrantType:        ErrUnsupportedGrantType,
	codeInvalidGrant:                ErrInvalidGrant,
	codeInvalidTarget:               ErrInvalidTarget,
	codeInvalidScope:                ErrInvalidScope,
	codeInvalidAuthorizationDetails: ErrInvalidAuthorizationDetails,
	codeInvalidRequest:              ErrInvalidRequest,
	codeServerError:                 ErrServerError,
}

// ResponseError is an error response a token endpoint answered with, as
// ParseErrorResponse reads it: its status, code and description as the
// server sent them, the description being the server's text, for a log. It
// wraps the sentinel of its code, so errors.Is, limes.CodeOf, CategoryOf
// and ProblemOf answer it as the endpoint answered its own refusal; a code
// this package does not know, or a body that is no error response, wraps
// ErrServerError.
type ResponseError struct {
	Response Error
	sentinel error
}

// Error says what the endpoint answered.
func (e *ResponseError) Error() string {
	return fmt.Sprintf("%v: the token endpoint answered %d %q: %q", e.sentinel, e.Response.Status, e.Response.Code, e.Response.Description)
}

// Unwrap is the sentinel of the response's code.
func (e *ResponseError) Unwrap() error { return e.sentinel }

// ParseErrorResponse reads resp, a token endpoint's answer, as a client of
// it: nil for a success (2xx), and otherwise a *ResponseError of its RFC
// 6749 §5.2 body, read up to MaxErrorResponseBytes. It leaves resp.Body for
// the caller to close.
func ParseErrorResponse(resp *http.Response) error {
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return nil
	}
	var body Error
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxErrorResponseBytes))
	if err == nil {
		err = json.Unmarshal(raw, &body)
	}
	body.Status = resp.StatusCode
	sentinel, known := responseSentinels[body.Code]
	if err != nil || !known {
		sentinel = ErrServerError
	}
	return &ResponseError{Response: body, sentinel: sentinel}
}
