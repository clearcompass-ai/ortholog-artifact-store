package staging

import "os"

// unsetEnvDirect removes the named env var from the process environment
// outright, so subsequent os.Getenv calls return "". Used by
// credentials_test.go's clearEnv helper because t.Setenv leaves the
// variable as the empty string (which still counts as "set" for
// purposes of POSIX getenv but reads as "" in Go) — for credential
// validation we want true absence, not empty-string presence.
func unsetEnvDirect(key string) error {
	return os.Unsetenv(key)
}
