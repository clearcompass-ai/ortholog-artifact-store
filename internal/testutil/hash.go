package testutil

import "crypto/sha256"

// hashSHA256 returns the raw 32-byte SHA-256 digest of data.
// Shared helper for httpfakes.go (synthetic CID generation) and
// cidvectors.go (test vector verification).
func hashSHA256(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}
