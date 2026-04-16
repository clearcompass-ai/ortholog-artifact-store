//go:build integration

package integration

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"
)

// randomPrefix returns a bucket-key prefix unique to each test.
// Lets multiple parallel conformance runs share a single bucket without
// stepping on each other's keys, which matters because container startup
// cost dwarfs test cost and we aggressively reuse containers.
func randomPrefix(t *testing.T) string {
	t.Helper()
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	// Replace / in the test name so prefixes stay valid object-key segments.
	name := strings.ReplaceAll(t.Name(), "/", "_")
	return "it/" + name + "/" + hex.EncodeToString(buf[:]) + "/"
}
