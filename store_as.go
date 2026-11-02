package limes

import (
	"fmt"
	"strings"

	"github.com/oleg-tkachuk/limes/internal/optional"
)

// As is s as the optional interface T beside Store —
// limes.As[limes.RevocationLineageLookup](store) — or ErrInvalidConfig
// naming T when s is not one. A store, or a decorator over one, that leaves
// an optional interface out turns its feature off with no error until it is
// used; As refuses that where it is called, at startup.
func As[T any](s Store) (T, error) {
	var t T
	if err := AsAll(s, &t); err != nil {
		return t, err
	}
	return t, nil
}

// AsAll sets each target, a pointer to an optional interface beside Store,
// to s, or refuses with ErrInvalidConfig naming every one s is not — one
// call at startup for every interface a consumer uses. A target that is no
// pointer to an interface is ErrInvalidRequest, and none is set.
func AsAll(s Store, targets ...any) error {
	missing, err := optional.Assign(s, nil, ErrInvalidRequest, targets...)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: the record store lacks %s", ErrInvalidConfig, strings.Join(missing, ", "))
	}
	return nil
}
