package storetest

import (
	"bytes"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
)

// compareUUID orders ids as a uuid column does: bytewise.
func compareUUID(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) }

// diffCapabilities names the first field in which got differs from want, or
// returns "" when none does. Times compare as instants, whatever their zone.
func diffCapabilities(got, want limes.Capability) string {
	times := []struct {
		name      string
		got, want time.Time
	}{
		{"IssuedAt", got.IssuedAt, want.IssuedAt},
		{"NotBefore", got.NotBefore, want.NotBefore},
		{"ExpiresAt", got.ExpiresAt, want.ExpiresAt},
	}
	for _, f := range times {
		if !f.got.Equal(f.want) {
			return fmt.Sprintf("%s = %v, want %v", f.name, f.got, f.want)
		}
	}
	got.IssuedAt, got.NotBefore, got.ExpiresAt = want.IssuedAt, want.NotBefore, want.ExpiresAt
	if !reflect.DeepEqual(got, want) {
		return fmt.Sprintf("\n got %+v\nwant %+v", got, want)
	}
	return ""
}
