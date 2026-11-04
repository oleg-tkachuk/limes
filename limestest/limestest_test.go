package limestest_test

import (
	"fmt"
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/limestest"
)

// recorder is a testing.TB that keeps what a check reports.
type recorder struct {
	testing.TB
	errors []string
}

func (r *recorder) Helper() {}

func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

// current is the table that pins limes as it answers now.
func current() map[limes.Code]limestest.Answer {
	out := map[limes.Code]limestest.Answer{}
	for _, code := range limes.Codes() {
		p, _ := limes.ProblemOfCode(code)
		out[code] = limestest.Answer{Status: p.Status, Category: p.Category}
	}
	return out
}

// checkCodes is what CheckCodes reports for pinned.
func checkCodes(t *testing.T, pinned map[limes.Code]limestest.Answer) []string {
	r := &recorder{TB: t}
	limestest.CheckCodes(r, pinned)
	return r.errors
}

func TestCheckCodes(t *testing.T) {
	if got := checkCodes(t, current()); len(got) != 0 {
		t.Fatalf("limes as it answers now: %q", got)
	}
	if got := current()[limes.CodeRevoked]; got != (limestest.Answer{Status: http.StatusUnauthorized, Category: limes.CategoryUnauthenticated}) {
		t.Errorf("revoked is answered %+v", got)
	}
	for name, tc := range map[string]struct {
		edit func(map[limes.Code]limestest.Answer)
		want []string
	}{
		"a category changed": {
			func(m map[limes.Code]limestest.Answer) {
				m[limes.CodeRevoked] = limestest.Answer{Status: http.StatusUnauthorized, Category: limes.CategoryUnavailable}
			},
			[]string{`"revoked" is answered limestest.Answer{Status:401, Category:"unauthenticated"}; pinned limestest.Answer{Status:401, Category:"unavailable"}`},
		},
		"a code not pinned": {
			func(m map[limes.Code]limestest.Answer) { delete(m, limes.CodeChargeNotFound) },
			[]string{`"charge_not_found" is not pinned; pin it, mapped: "charge_not_found": limestest.Answer{Status:404, Category:"not_found"},`},
		},
		"a code limes no longer answers": {
			func(m map[limes.Code]limestest.Answer) { m["gone_code"] = limestest.Answer{} },
			[]string{`"gone_code" is pinned, and limes no longer answers it`},
		},
	} {
		pinned := current()
		tc.edit(pinned)
		assertReported(t, name, checkCodes(t, pinned), tc.want)
	}
}

// assertReported fails t unless got says each of want, in order.
func assertReported(t *testing.T, name string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: reported %q, want %d", name, got, len(want))
		return
	}
	for i, w := range want {
		if !strings.Contains(got[i], w) {
			t.Errorf("%s: reported %q, want it to say %q", name, got[i], w)
		}
	}
}

// transportCode is a transport's own code, as a consumer's RPC framework
// has them.
type transportCode int

// The transport's codes this test maps to.
const (
	transportOther transportCode = iota
	transportUnauthenticated
	transportNotFound
)

// String names the code, as an RPC framework's codes do.
func (c transportCode) String() string {
	switch c {
	case transportUnauthenticated:
		return "unauthenticated"
	case transportNotFound:
		return "not_found"
	default:
		return "other"
	}
}

// toTransport is a consumer's mapping, by category.
func toTransport(code limes.Code) transportCode {
	switch c, _ := limes.CategoryOfCode(code); c {
	case limes.CategoryUnauthenticated:
		return transportUnauthenticated
	case limes.CategoryNotFound:
		return transportNotFound
	default:
		return transportOther
	}
}

func TestCheckMapping(t *testing.T) {
	codes := limes.CodesOf(limes.OriginLimes())
	pinned := map[limes.Code]transportCode{}
	for _, c := range codes {
		pinned[c] = toTransport(c)
	}
	check := func(pinned map[limes.Code]transportCode, answer func(limes.Code) transportCode) []string {
		r := &recorder{TB: t}
		limestest.CheckMapping(r, codes, pinned, answer)
		return r.errors
	}
	if got := check(pinned, toTransport); len(got) != 0 {
		t.Fatalf("the mapping as pinned: %q", got)
	}
	if pinned[limes.CodeRevoked] != transportUnauthenticated || pinned[limes.CodeNotFound] != transportNotFound {
		t.Fatalf("the mapping pins revoked %v, not_found %v", pinned[limes.CodeRevoked], pinned[limes.CodeNotFound])
	}
	moved := func(code limes.Code) transportCode {
		if code == limes.CodeRevoked {
			return transportOther
		}
		return toTransport(code)
	}
	assertReported(t, "a code the mapping answers otherwise", check(pinned, moved),
		[]string{`"revoked" is answered 0 /* other */; pinned 1 /* unauthenticated */: map it anew, then pin "revoked": 0 /* other */,`})

	without := maps.Clone(pinned)
	delete(without, limes.CodeNotFound)
	assertReported(t, "a code not pinned", check(without, toTransport), []string{`"not_found" is not pinned; pin it, mapped: "not_found": 2 /* not_found */,`})

	outside := maps.Clone(pinned)
	outside[limes.CodeInvalidGrant] = transportUnauthenticated
	outside["gone_code"] = transportOther
	assertReported(t, "codes pinned outside codes", check(outside, toTransport), []string{
		`"gone_code" is pinned, and limes no longer answers it`,
		`"invalid_grant" is pinned, and not among the codes checked`,
	})
}

func TestCheckCategories(t *testing.T) {
	mapped := map[limes.Category]bool{}
	for _, c := range limes.Categories() {
		mapped[c] = true
	}
	check := func(m map[limes.Category]bool) []string {
		r := &recorder{TB: t}
		limestest.CheckCategories(r, func(c limes.Category) bool { return m[c] })
		return r.errors
	}
	if got := check(mapped); len(got) != 0 {
		t.Fatalf("a mapping of every category: %q", got)
	}
	without := maps.Clone(mapped)
	delete(without, limes.CategoryPrecondition)
	got := check(without)
	if len(got) != 1 || !strings.Contains(got[0], `category "precondition"`) {
		t.Errorf("a mapping without precondition: reported %q", got)
	}
}

// wireName is a transport's code that is a string already, and names itself.
type wireName string

func (w wireName) String() string { return string(w) }

// A code that is a string reads as itself, without its name repeated.
func TestCheckMappingNamesAStringOnce(t *testing.T) {
	r := &recorder{TB: t}
	limestest.CheckMapping(r, []limes.Code{limes.CodeRevoked}, map[limes.Code]wireName{}, func(limes.Code) wireName { return "unauthenticated" })
	assertReported(t, "a string code not pinned", r.errors, []string{`pin it, mapped: "revoked": "unauthenticated",`})
	if len(r.errors) == 1 && strings.Contains(r.errors[0], "/*") {
		t.Errorf("reported %q: its name beside a string", r.errors[0])
	}
}

// A check of no codes fails, whatever is pinned.
func TestCheckMappingOfNoCodes(t *testing.T) {
	for name, pinned := range map[string]map[limes.Code]transportCode{
		"nothing pinned": {},
		"a code pinned":  {limes.CodeRevoked: transportUnauthenticated},
	} {
		r := &recorder{TB: t}
		limestest.CheckMapping(r, nil, pinned, toTransport)
		if len(r.errors) == 0 || !strings.Contains(r.errors[0], "no codes to check") {
			t.Errorf("%s: reported %q, want no codes to check first", name, r.errors)
		}
	}
}
