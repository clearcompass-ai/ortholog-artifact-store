package staging

import (
	"fmt"
	"os"
	"strings"
)

// credentialGroup describes one Wave 3 vendor's required env vars.
// The Wave 3 entry point validates that for any vendor where ANY env
// var is set, ALL listed env vars must be set — a partial credential
// set is always a misconfiguration. A vendor where no env vars are
// set is "not enabled in this run" and validation passes silently.
//
// Lives in a non-tagged file so it can be unit-tested under
// `go test ./tests/staging/` even when -tags=staging is not active.
// The Wave 3 binary itself (main_test.go's TestMain) is the only
// production caller.
type credentialGroup struct {
	name string
	keys []string
}

// validate checks that either ALL env vars in g.keys are set or NONE
// are. Returns a descriptive error naming the absent keys when the
// set is partial; nil otherwise.
func (g credentialGroup) validate() error {
	var present, absent []string
	for _, k := range g.keys {
		if os.Getenv(k) != "" {
			present = append(present, k)
		} else {
			absent = append(absent, k)
		}
	}
	if len(present) == 0 {
		return nil // vendor not enabled, fine
	}
	if len(absent) > 0 {
		return fmt.Errorf("%s partially configured; missing: %s",
			g.name, strings.Join(absent, ", "))
	}
	return nil
}
