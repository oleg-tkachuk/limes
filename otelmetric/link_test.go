package otelmetric_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The guard binaries, one per wrapper, under internal/linkguard.
const (
	linkguardRequests   = "./internal/linkguard/requests"
	linkguardInstrument = "./internal/linkguard/instrument"
)

// usageStoreOnly are symbols of Instrument's decorator: its methods for
// what a meter.UsageStore does beside a Bump, the optional interfaces it
// forwards, and the instruments of a charge. A binary that wraps a store
// in InstrumentRequests alone links none of them.
var usageStoreOnly = []string{
	"otelmetric.(*meteringStore).Charge",
	"otelmetric.(*meteringStore).Settle",
	"otelmetric.(*meteringStore).Reserve",
	"otelmetric.(*meteringStore).Refund",
	"otelmetric.withOptional",
	"otelmetric.(*meteringInstruments).recordCharge",
}

// InstrumentRequests links its Bump alone. A binary built with it holds
// none of the symbols Instrument's decorator adds, though it reflects on
// methods by name and so keeps every exported method of each type it puts
// in an interface; the same binary built with Instrument holds them all,
// so the check is one that sees them.
func TestInstrumentRequestsLinksBumpAlone(t *testing.T) {
	if testing.Short() {
		t.Skip("builds two binaries")
	}
	requests, instrument := symbolsOf(t, linkguardRequests), symbolsOf(t, linkguardInstrument)
	for _, sym := range usageStoreOnly {
		if !strings.Contains(instrument, sym) {
			t.Errorf("a binary built with Instrument lacks %s: the check no longer sees what it guards", sym)
		}
		if strings.Contains(requests, sym) {
			t.Errorf("a binary built with InstrumentRequests alone links %s", sym)
		}
	}
}

// symbolsOf builds the command pkg and returns its symbol table.
func symbolsOf(t *testing.T, pkg string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), filepath.Base(pkg))
	if out, err := exec.CommandContext(t.Context(), "go", "build", "-o", bin, pkg).CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", pkg, err, out)
	}
	out, err := exec.CommandContext(t.Context(), "go", "tool", "nm", bin).Output()
	if err != nil {
		t.Fatalf("go tool nm %s: %v", bin, err)
	}
	return string(out)
}
