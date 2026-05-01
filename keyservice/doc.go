// Package keyservice provides production implementations of the SDK's
// lifecycle/artifact.ArtifactKeyService contract.
//
// The SDK declares the operation-oriented contract (encrypt, wrap for
// recipient, decrypt, rotate, delete by CID handle). This package
// supplies concrete backends — HSM-backed and envelope-encryption —
// that domain networks (judicial, recording, credentialing) consume
// via the SDK interface only. Domain code never imports cloud SDKs;
// they all live here, behind one shared interface.
//
// # Tier 1 — HashiCorp Vault Transit (OSS) — vault.go
//
// Envelope encryption against Vault Transit's aes256-gcm96 KEK. Per
// artifact, the impl: creates a transit key named after the artifact
// CID, calls datakey/plaintext to obtain a fresh DEK + KEK-wrapped
// DEK in one round-trip, AES-GCM-encrypts the artifact in process,
// stores the wrapped DEK in Vault kv-v2 indexed by CID, and zeroizes
// the in-memory DEK before returning.
//
// Recipient wrap: re-fetch wrapped DEK from kv-v2, ask Transit to
// decrypt → DEK in process briefly → ECIES wrap to recipient pubkey
// (via crypto/escrow) → zeroize DEK → return envelope. Recipient
// unwraps with their own secp256k1 private key; never calls Vault.
//
// Cryptographic erasure: Delete removes both the per-artifact transit
// key and the kv-v2 wrapping. Vault's transit DELETE is a true
// secure-delete (the key versions are unrecoverable). Without the KEK,
// the wrapped DEK in kv-v2 is opaque garbage; without the wrapped DEK,
// the on-storage ciphertext is undecryptable.
//
// TrustClass: ClassEnvelope. The DEK appears in process memory for
// milliseconds during WrapForRecipient and Decrypt. The KEK never
// appears in process memory.
//
// Cost: ~$25/month for HCP Vault Standard, $0 self-hosted on a small
// VM. See artifact.RunConformance — vault_test.go runs it end-to-end
// against a real Vault dev-mode subprocess (no mocks).
//
// # Tier 1.5 — PKCS#11 / SoftHSM2 — pkcs11.go (planned)
//
// Generic on-prem HSM via PKCS#11. Same operation set, different trust
// boundary (ClassHSMTrue when the HSM supports CKM_ECDH1_DERIVE +
// CKM_AES_KEY_WRAP_PAD for secp256k1; falls back to envelope mode
// otherwise). SoftHSM2 is the test-mode driver; production swaps to
// Thales Luna, Equinix SmartKey, or Fortanix DSM with no code change.
//
// # Tier 2 — Cloud HSMs — awscloudhsm.go / azure_managedhsm.go (future)
//
// Direct PKCS#11 against AWS CloudHSM and Azure Managed HSM. Same
// interface; defer until an auditor or contract requires FIPS 140-2
// Level 3 attestation.
//
// # Conformance
//
// Every implementation in this package runs the SDK's
// artifact.RunConformance test suite. A backend ships when that
// passes; no per-backend test rewrites. Run via:
//
//	cd ortholog-artifact-store/keyservice
//	go test -v ./...
package keyservice
