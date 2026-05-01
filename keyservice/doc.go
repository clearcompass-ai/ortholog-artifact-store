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
// # Tier 1.5 — PKCS#11 / SoftHSM2 — pkcs11.go
//
// Generic on-prem HSM via PKCS#11 (miekg/pkcs11 binding). Per-artifact
// AES-256 key generated and persisted on the token; CKM_AES_GCM
// encrypts in-place (key never leaves HSM on the encrypt and decrypt
// hot paths). Recipient wrap extracts the key once for ECIES (no
// PKCS#11 module ships with secp256k1 ECDH+AES-KEY-WRAP-PAD natively;
// extracting for ECIES is the standards-track workaround).
//
// CID is committed as the per-key CKA_LABEL after ciphertext is
// known; nonce piggybacks on CKA_ID. Cryptographic erasure is a
// CKO_DESTROY of the per-artifact key — the token's persistent store
// no longer contains it, and SoftHSM2/HSMs never expose the wrapped
// version, so the on-storage ciphertext becomes opaque garbage.
//
// TrustClass: ClassEnvelope (recipient-wrap path holds the key
// briefly in process). Decrypt path is HSM-resident throughout.
//
// SoftHSM2 is the test-mode driver; the same code targets Thales
// Luna, Equinix SmartKey, Fortanix DSM, AWS CloudHSM, and Azure
// Managed HSM with no code changes — only ModulePath/TokenLabel/Pin
// configuration. See artifact.RunConformance — pkcs11_test.go runs
// it end-to-end against a real SoftHSM2 token (no mocks).
//
// # Tier 2 — GCP Cloud KMS + Firestore — gcpkms.go
//
// Managed cloud HSM via GCP Cloud KMS (KEK in HSM, FIPS 140-2 Level 3
// at HSM protection level) + Firestore as the wrapped-DEK store.
// Single global KEK pattern: per-artifact AES-256 DEK is generated
// locally, wrapped under the KEK via cryptoKeys.encrypt, persisted
// to Firestore at <collection>/<cid-hex>. Cryptographic erasure is
// the Firestore document delete — without the wrapped blob the KEK
// alone cannot reproduce the DEK, so the on-storage ciphertext
// becomes opaque garbage.
//
// TrustClass: ClassEnvelope (DEK appears briefly in process for
// AES-GCM ops + ECIES recipient wrap; KEK never leaves Cloud KMS).
//
// Cost: ~$1/month for the KEK at HSM protection level (one global
// key, not per-artifact); ~$0.06/month at SOFTWARE protection level.
// Cryptographic operations: ~$0.03 per 10K. Firestore writes/reads
// at standard rates. At 1M artifact writes/month: <$5 total.
//
// Auth: production uses Application Default Credentials (ADC) via
// golang.org/x/oauth2/google. Tests use in-process httptest.Server
// fakes that implement the relevant subset of the Cloud KMS REST
// API (Encrypt, Decrypt) and Firestore REST API (CreateDocument,
// GetDocument, DeleteDocument) — same hermetic style as the Vault
// dev-mode subprocess pattern.
//
// # Tier 2.5 — Cloud HSMs — awscloudhsm.go / azure_managedhsm.go (future)
//
// Direct PKCS#11 against AWS CloudHSM and Azure Managed HSM. Same
// interface; defer until an auditor or contract requires
// vendor-specific FIPS 140-2 Level 3 attestation that GCP Cloud
// KMS HSM does not satisfy.
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
