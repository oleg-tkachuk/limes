package limes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"testing"
)

// failingForm is fakeForm whose Open, or else whose unseal, fails with err:
// a form's error as the verifier receives it.
type failingForm struct {
	openErr, unsealErr error
}

func (f failingForm) Seal(ctx context.Context, sign func(context.Context, ed25519.PublicKey) (string, error)) (string, error) {
	return fakeForm{}.Seal(ctx, sign)
}

func (f failingForm) Open(token string) (string, func(ed25519.PublicKey) (Unsealed, error), error) {
	if f.openErr != nil {
		return "", nil, f.openErr
	}
	sealed, _, err := fakeForm{}.Open(token)
	return sealed, func(ed25519.PublicKey) (Unsealed, error) { return Unsealed{}, f.unsealErr }, err
}

// errFormOwn is a form's own sentinel, which the verifier does not pass on.
var errFormOwn = errors.New("form: own sentinel")

// A form's error reaches the caller in one family, named once in its text:
// the form's own text when it wraps the family already, the family and the
// form's text when it does not.
func TestFormRefusalNamesItsFamilyOnce(t *testing.T) {
	const cause = "biscuit encoding: illegal base64 data at input byte 8"
	cases := map[string]struct {
		form    failingForm
		family  error
		message string
	}{
		"open wraps the token family": {
			form:    failingForm{openErr: fmt.Errorf("%w: %s", ErrInvalidToken, cause)},
			family:  ErrInvalidToken,
			message: ErrInvalidToken.Error() + ": " + cause,
		},
		"open returns a bare cause": {
			form:    failingForm{openErr: errors.New(cause)},
			family:  ErrInvalidToken,
			message: ErrInvalidToken.Error() + ": " + cause,
		},
		"open wraps a sentinel of its own": {
			form:    failingForm{openErr: fmt.Errorf("%w: %s", errFormOwn, cause)},
			family:  ErrInvalidToken,
			message: ErrInvalidToken.Error() + ": " + errFormOwn.Error() + ": " + cause,
		},
		"unseal wraps the attenuation family": {
			form:    failingForm{unsealErr: fmt.Errorf("%w: block 1: %s", ErrBiscuitAttenuation, cause)},
			family:  ErrBiscuitAttenuation,
			message: ErrBiscuitAttenuation.Error() + ": block 1: " + cause,
		},
		"unseal wraps the token family": {
			form:    failingForm{unsealErr: fmt.Errorf("%w: %s", ErrInvalidToken, cause)},
			family:  ErrInvalidToken,
			message: ErrInvalidToken.Error() + ": " + cause,
		},
		"unseal returns a bare cause": {
			form:    failingForm{unsealErr: errors.New(cause)},
			family:  ErrInvalidToken,
			message: ErrInvalidToken.Error() + ": " + cause,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, v, _, _, token := biscuitFixture(t)
			v.cfg.Biscuit.Form = c.form
			_, err := v.Verify(context.Background(), VerifyRequest{Token: token, Audience: audienceData})
			if !errors.Is(err, c.family) {
				t.Fatalf("err = %v, want %v", err, c.family)
			}
			if errors.Is(err, errFormOwn) {
				t.Errorf("err = %v still matches the form's own sentinel", err)
			}
			if c.family == ErrInvalidToken && errors.Is(err, ErrBiscuitAttenuation) {
				t.Errorf("err = %v matches ErrBiscuitAttenuation", err)
			}
			if err.Error() != c.message {
				t.Errorf("message = %q, want %q", err.Error(), c.message)
			}
		})
	}
}
