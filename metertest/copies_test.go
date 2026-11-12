package metertest_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/limes/meter"
	"github.com/oleg-tkachuk/limes/metertest"
)

// A store that reads no copy counters back skips the checks that read them,
// rather than passing them unchecked. Each check runs in a child test
// process against a memstore usage store hiding its CopyUsageReader.

// noCopyReaderEnv names the check a child process runs against the store
// without a CopyUsageReader.
const noCopyReaderEnv = "LIMES_METERTEST_NO_COPY_READER_CHECK"

// noCopyReaderRunName is the test a child process runs the suite in.
const noCopyReaderRunName = "TestRunSkipsCopyChecksWithoutACopyUsageReader"

// noCopyReaderChecks are the checks that read copy counters back.
var noCopyReaderChecks = []string{
	"ReleasingAHoldReturnsItToTheCopy",
	"OverrunRecordCrossesACopyBudget",
	"ACopyCountsForItsCapabilityAndGoesWithIt",
	"ACopyIDSharedByTwoCapabilitiesCountsApart",
	"CopyUsageRefusesAnInvalidRequest",
}

// noCopyReaderStore is an Env over a memstore usage store seen through
// meter.UsageStore alone, so it is no meter.CopyUsageReader.
func noCopyReaderStore(t *testing.T) metertest.Env {
	t.Helper()
	env := perSubjectStore(t)
	env.Usage = struct{ meter.UsageStore }{env.Usage.(*perSubject).UsageStore}
	return env
}

func TestRunSkipsCopyChecksWithoutACopyUsageReader(t *testing.T) {
	if os.Getenv(noCopyReaderEnv) != "" {
		metertest.Run(t, noCopyReaderStore)
		return
	}
	for _, check := range noCopyReaderChecks {
		t.Run(check, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^"+noCopyReaderRunName+"$/^"+check+"$", "-test.v")
			cmd.Env = append(os.Environ(), noCopyReaderEnv+"="+check)
			out, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(out), "--- SKIP: "+noCopyReaderRunName+"/"+check) {
				t.Fatalf("a store without a CopyUsageReader did not skip %s (err %v):\n%s", check, err, out)
			}
		})
	}
}
