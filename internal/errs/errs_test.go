package errs

import (
	"errors"
	"io"
	"testing"
)

// errRevoked is a sentinel whose text, like every limes sentinel's, opens
// with the module's prefix.
var errRevoked = errors.New(modulePrefix + "revoked")

func TestErrorfNamesThePrefixOnce(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
		is   []error
	}{
		{"context around a limes error", Errorf("limes: reissue %s: %w", "x", errRevoked),
			"limes: reissue x: revoked", []error{errRevoked}},
		{"a family around a limes error", Errorf("%w: %w", ErrInvalidRequest, errRevoked),
			"limes: invalid request: revoked", []error{ErrInvalidRequest, errRevoked}},
		{"a family around another package's error", Errorf("%w: dpop: key: %w", ErrInvalidRequest, io.EOF),
			"limes: invalid request: dpop: key: EOF", []error{ErrInvalidRequest, io.EOF}},
		{"a limes error nested twice", Errorf("limes: delegate: %w", Errorf("limes: lookup: %w", errRevoked)),
			"limes: delegate: lookup: revoked", []error{errRevoked}},
		{"no limes error at all", Errorf("dpop: key: %w", io.EOF),
			"dpop: key: EOF", []error{io.EOF}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Errorf("Error() = %q, want %q", got, tc.want)
			}
			for _, target := range tc.is {
				if !errors.Is(tc.err, target) {
					t.Errorf("%q does not match %q", tc.err, target)
				}
			}
		})
	}
}

// A family's message that quotes a limes error names the prefix once too.
func TestFamilyNamesThePrefixOnce(t *testing.T) {
	err := Request("limes: parent: %v", errRevoked)
	if want := "limes: invalid request: parent: revoked"; err.Error() != want {
		t.Errorf("Error() = %q, want %q", err, want)
	}
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("%q does not match ErrInvalidRequest", err)
	}
}
