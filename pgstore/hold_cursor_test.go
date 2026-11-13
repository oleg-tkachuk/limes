package pgstore

import (
	"errors"
	"strconv"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
)

// holdCursorAt is a hold cursor at sec, its microseconds zero.
func holdCursorAt(sec int64) string {
	return strconv.FormatInt(sec, 10) + holdCursorSep + "0" + holdCursorSep + uuid.Nil().String()
}

// A hold cursor's time is read only within the years a time is formatted
// in and every expiry is written in: one past them is the client's error,
// limes.ErrInvalidRequest, never a time handed to PostgreSQL, whose
// timestamptz it may leave (FuzzCursors found 10000000000000 seconds).
func TestParseHoldPlaceBoundsItsTime(t *testing.T) {
	for name, tc := range map[string]struct {
		sec int64
		ok  bool
	}{
		"the first instant":      {sec: cursorFirstUnix, ok: true},
		"a second before it":     {sec: cursorFirstUnix - 1},
		"the last instant":       {sec: cursorLastUnix, ok: true},
		"a second past it":       {sec: cursorLastUnix + 1},
		"what FuzzCursors found": {sec: 10000000000000},
	} {
		place, err := parseHoldPlace(holdCursorAt(tc.sec))
		if !tc.ok {
			if !errors.Is(err, limes.ErrInvalidRequest) {
				t.Errorf("%s: err = %v, want ErrInvalidRequest", name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got := place.expires.Unix(); got != tc.sec {
			t.Errorf("%s: read as %d seconds", name, got)
		}
		if again, err := parseHoldPlace(place.String()); err != nil || !again.expires.Equal(place.expires) {
			t.Errorf("%s: reads back as %v, %v", name, again.expires, err)
		}
	}
	if got := time.Unix(cursorLastUnix, 0).UTC().Year(); got != cursorLastYear {
		t.Errorf("the last instant is in year %d, want %d", got, cursorLastYear)
	}
}
