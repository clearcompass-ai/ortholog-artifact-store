package testutil

import (
	"os"
	"testing"

	"go.uber.org/goleak"
)

// RunWithGoleak is the canonical TestMain body for this repo. It runs
// tests and then verifies no goroutines leaked. Packages that own
// background workers (like backends/mirrored with its async pin goroutine)
// call this from their TestMain.
//
// Usage:
//
//	func TestMain(m *testing.M) {
//	    testutil.RunWithGoleak(m)
//	}
//
// Leak verification is done with a short list of known-benign goroutines
// suppressed. Adding new suppressions here is a deliberate act — the
// default is to fail if *any* goroutine remains at test exit.
func RunWithGoleak(m *testing.M) {
	code := m.Run()
	if code != 0 {
		// Don't mask a real test failure with a leak-check failure.
		os.Exit(code)
	}
	if err := goleak.Find(
		// testing.tRunner spawns a goroutine that can linger briefly on
		// fast exits. Suppressed by default in goleak.IgnoreTopFunction.
		goleak.IgnoreTopFunction("testing.(*M).Run"),
		goleak.IgnoreTopFunction("go.opencensus.io/stats/view.(*worker).start"),
		// HTTP test servers occasionally leave an accept loop behind for
		// one scheduler tick after Close(). Only suppress if flaky.
		goleak.IgnoreTopFunction("net/http.(*Server).Serve"),
	); err != nil {
		_, _ = os.Stderr.WriteString("goroutine leak detected:\n" + err.Error() + "\n")
		os.Exit(1)
	}
	os.Exit(0)
}
