package config

import "os"

// Thin wrappers so config_test.go can reference env operations without
// `os` imports cluttering up every case. Kept in a separate file so
// production code isn't polluted with test-only indirection.

func osLookupEnv(k string) (string, bool) { return os.LookupEnv(k) }
func osSetenv(k, v string) error          { return os.Setenv(k, v) }
func osUnsetenv(k string) error           { return os.Unsetenv(k) }
