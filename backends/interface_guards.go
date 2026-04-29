package backends

import "github.com/clearcompass-ai/ortholog-sdk/storage"

// ─── Compile-time interface assertions ────────────────────────────────
//
// These zero-cost declarations make Go's type checker fail the build
// when a concrete BackendProvider stops satisfying its declared
// interfaces. The conformance suite catches semantic deviations
// (Push returns the wrong error, Fetch loses bytes), but a missing
// or mistyped method only surfaces at the call site that uses it —
// often a backend constructor in main.go that compiles fine until
// someone tries to *use* the new backend.
//
// Pinning the contract here moves that failure forward by 50 ms:
// the build breaks before the binary even links.
//
// Two contracts each:
//   storage.ContentStore  — Push/Fetch/Pin/Exists/Delete (SDK contract)
//   BackendProvider       — adds Resolve + Healthy (artifact-store contract)
//
// One declaration per concrete type, in a single file, so the audit
// surface is one grep target. Adding a new backend is a one-line
// additive change in this file plus its own conformance run.

// ── InMemoryBackend ──────────────────────────────────────────────────
var (
	_ storage.ContentStore = (*InMemoryBackend)(nil)
	_ BackendProvider      = (*InMemoryBackend)(nil)
)

// ── GCSBackend ───────────────────────────────────────────────────────
var (
	_ storage.ContentStore = (*GCSBackend)(nil)
	_ BackendProvider      = (*GCSBackend)(nil)
)

// ── RustFSBackend ────────────────────────────────────────────────────
var (
	_ storage.ContentStore = (*RustFSBackend)(nil)
	_ BackendProvider      = (*RustFSBackend)(nil)
)

// ── MirroredStore ────────────────────────────────────────────────────
var (
	_ storage.ContentStore = (*MirroredStore)(nil)
	_ BackendProvider      = (*MirroredStore)(nil)
)
