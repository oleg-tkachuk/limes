package limes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// How a protected resource answers a request this module refused: a status,
// a WWW-Authenticate challenge as RFC 6750 §3 (Bearer) and RFC 9449 §7.1
// (DPoP) define it, and an RFC 9457 problem details body. Every service
// built on the module then refuses alike, and a client reads the refusal the
// standards' way rather than one service's.
//
// The descriptions are fixed per kind of refusal: an error's own text can
// name kids, issuers or stores, which is for the server's log, not the
// client.

// WWWAuthenticateHeader is the response header a challenge travels in.
const WWWAuthenticateHeader = "WWW-Authenticate"

// MediaTypeProblem is RFC 9457 §3's media type for problem details.
const MediaTypeProblem = "application/problem+json"

// problemTypeBase prefixes the problem type URIs this module defines.
const problemTypeBase = "https://github.com/oleg-tkachuk/limes/problems/"

// Error codes of RFC 6750 §3.1 and RFC 9449 §7.1, §8.
const (
	codeInvalidRequest    = "invalid_request"
	codeInvalidToken      = "invalid_token"
	codeInsufficientScope = "insufficient_scope"
	codeInvalidDPoPProof  = "invalid_dpop_proof"
	codeUseDPoPNonce      = "use_dpop_nonce"
)

// Problem is an RFC 9457 problem details object.
type Problem struct {
	// Type is a URI naming the kind of problem.
	Type string `json:"type"`
	// Title is a short, fixed summary of the kind.
	Title string `json:"title"`
	// Status repeats the HTTP status.
	Status int `json:"status"`
	// Detail explains this occurrence, in terms safe to show the client.
	Detail string `json:"detail,omitempty"`
}

// ErrorResponse is how to answer a refused request.
type ErrorResponse struct {
	// Status is the HTTP status.
	Status int
	// Challenges are the WWW-Authenticate values, one per scheme; none for a
	// refusal that is not about authentication.
	Challenges []string
	// DPoPNonce is the nonce to send in the DPoP-Nonce header, when the
	// refusal asks for one.
	DPoPNonce string
	// Problem is the body.
	Problem Problem
}

// refusal is one kind of refusal and how it is answered.
type refusal struct {
	status    int
	code      string // RFC 6750 / RFC 9449 error code; "" for no challenge
	dpop      bool   // the challenge is DPoP's
	slug      string // the problem type, after problemTypeBase
	title     string
	sentinels []error
}

// refusals is checked in order: the most specific kind first, since a DPoP
// refusal also matches ErrInvalidSignature.
var refusals = []refusal{
	{http.StatusBadRequest, codeInvalidRequest, false, "malformed-authorization", "The Authorization header is malformed",
		[]error{ErrMalformedAuthorization}},
	{http.StatusUnauthorized, codeUseDPoPNonce, true, "dpop-nonce-required", "A DPoP proof with the server's nonce is required",
		[]error{ErrDPoPNonceRequired}},
	{http.StatusUnauthorized, codeInvalidDPoPProof, true, "invalid-dpop-proof", "The DPoP proof is missing or invalid",
		[]error{ErrDPoPRequired, ErrDPoPInvalid, ErrDPoPReplayed}},
	{http.StatusUnauthorized, codeInvalidToken, false, "invalid-token", "The access token is invalid",
		[]error{ErrAuthSchemeMismatch, ErrInvalidSignature, ErrExpired, ErrNotYetValid, ErrRevoked, ErrAudienceMismatch}},
	{http.StatusForbidden, codeInsufficientScope, false, "insufficient-scope", "The token does not allow this request",
		[]error{ErrCaveatViolation}},
	{http.StatusForbidden, "", false, "budget-exhausted", "The token's request or budget limit is exhausted",
		[]error{ErrBudgetExceeded, ErrTenantBudgetExceeded, ErrRequestLimitExceeded}},
}

// internalError answers any other error: the server's fault, said without
// its text.
var internalError = refusal{status: http.StatusInternalServerError, slug: "internal", title: "The request could not be authorised"}

// ErrorResponse is how to answer a request refused with err, presented
// under scheme. A refusal about the token is challenged under the scheme the
// client used (Bearer when it named none); one about the proof, under DPoP.
// A verifier with DPoP configured also advertises its algorithms in a DPoP
// challenge, and hands out a fresh nonce with use_dpop_nonce.
func (v *StandardVerifier) ErrorResponse(ctx context.Context, err error, scheme AuthScheme) ErrorResponse {
	r := internalError
	for _, candidate := range refusals {
		if matchesAny(err, candidate.sentinels) {
			r = candidate
			break
		}
	}
	resp := ErrorResponse{
		Status:  r.status,
		Problem: Problem{Type: problemTypeBase + r.slug, Title: r.title, Status: r.status},
	}
	if r.code == "" {
		return resp
	}
	dpopConfigured := v.cfg.DPoP != nil
	challengeDPoP := r.dpop || scheme == AuthSchemeDPoP
	if challengeDPoP {
		resp.Challenges = append(resp.Challenges, challenge(AuthSchemeDPoP, r.code, r.title, true))
	} else {
		resp.Challenges = append(resp.Challenges, challenge(AuthSchemeBearer, r.code, r.title, false))
		if dpopConfigured {
			resp.Challenges = append(resp.Challenges, challenge(AuthSchemeDPoP, "", "", true))
		}
	}
	if r.code == codeUseDPoPNonce {
		resp.DPoPNonce, _ = v.DPoPNonce(ctx) // without one the client retries and is refused again
	}
	return resp
}

func matchesAny(err error, sentinels []error) bool {
	for _, s := range sentinels {
		if errors.Is(err, s) {
			return true
		}
	}
	return false
}

// challenge is one WWW-Authenticate challenge (RFC 9110 §11.6.1): the scheme,
// then error and error_description (RFC 6750 §3) when there is an error, and
// for DPoP the accepted algorithms (RFC 9449 §7.1).
func challenge(scheme AuthScheme, code, description string, withAlgs bool) string {
	var params []string
	if code != "" {
		params = append(params, authParam("error", code), authParam("error_description", description))
	}
	if withAlgs {
		params = append(params, authParam("algs", strings.Join(DPoPSigningAlgorithms(), " ")))
	}
	if len(params) == 0 {
		return string(scheme)
	}
	return string(scheme) + " " + strings.Join(params, ", ")
}

// authParam is name="value" as a quoted-string (RFC 9110 §5.6.4), with only
// the characters RFC 6750 §3 allows in a value.
func authParam(name, value string) string {
	clean := strings.Map(func(r rune) rune {
		if r == 0x20 || r == 0x21 || r >= 0x23 && r <= 0x5B || r >= 0x5D && r <= 0x7E {
			return r
		}
		return -1
	}, value)
	return name + `="` + clean + `"`
}

// WriteErrorResponse writes resp: its status, challenges, nonce and problem.
func WriteErrorResponse(w http.ResponseWriter, resp ErrorResponse) {
	h := w.Header()
	for _, c := range resp.Challenges {
		h.Add(WWWAuthenticateHeader, c)
	}
	if resp.DPoPNonce != "" {
		h.Set(DPoPNonceHeader, resp.DPoPNonce)
	}
	body, err := json.Marshal(resp.Problem)
	if err != nil {
		http.Error(w, http.StatusText(resp.Status), resp.Status)
		return
	}
	h.Set("Content-Type", MediaTypeProblem)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	// No store: a refusal is about this request's credentials.
	h.Set(headerCacheControl, directiveNoStore)
	w.WriteHeader(resp.Status)
	_, _ = w.Write(body)
}
