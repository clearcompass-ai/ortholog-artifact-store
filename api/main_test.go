package api

import (
	"testing"

	"github.com/clearcompass-ai/ortholog-artifact-store/internal/testutil"
)

// TestMain runs all api package tests with goroutine leak detection.
// Any handler that launches goroutines must clean them up before the
// test binary exits. This is enforced across every test run.
func TestMain(m *testing.M) {
	testutil.RunWithGoleak(m)
}
