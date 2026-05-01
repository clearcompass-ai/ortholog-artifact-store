package keyservice

import (
	"errors"
	"fmt"

	"github.com/miekg/pkcs11"
)

// errPKCS11NotFound is the typed marker we use to translate a missing
// PKCS#11 object into artifact.ErrKeyNotFound at the operation level.
// Internal — never returned to callers directly.
var errPKCS11NotFound = errors.New("keyservice/pkcs11: object not found")

// p11Session bundles a session handle with the parent context. Open
// via openSession; always Close in a deferred call. Login is invoked
// inline with the user PIN — we do not maintain login across sessions
// because SoftHSM2 (and most modules) scope login state to the session
// in CKU_USER mode without an SO PIN holder.
type p11Session struct {
	ctx *pkcs11.Ctx
	sh  pkcs11.SessionHandle
}

// openSession opens a fresh R/W session against the configured slot
// and logs in as CKU_USER. Callers MUST defer s.Close() to release
// HSM-side state.
func (p *PKCS11) openSession() (*p11Session, error) {
	sh, err := p.ctx.OpenSession(p.slotID, pkcs11.CKF_SERIAL_SESSION|pkcs11.CKF_RW_SESSION)
	if err != nil {
		return nil, fmt.Errorf("keyservice/pkcs11: OpenSession: %w", err)
	}
	if err := p.ctx.Login(sh, pkcs11.CKU_USER, p.cfg.Pin); err != nil {
		_ = p.ctx.CloseSession(sh)
		// CKR_USER_ALREADY_LOGGED_IN is benign at the token level — a
		// previous session may have left the login persistent. Treat
		// as success and proceed.
		var pErr pkcs11.Error
		if errors.As(err, &pErr) && uint(pErr) == pkcs11.CKR_USER_ALREADY_LOGGED_IN {
			sh, oerr := p.ctx.OpenSession(p.slotID, pkcs11.CKF_SERIAL_SESSION|pkcs11.CKF_RW_SESSION)
			if oerr != nil {
				return nil, fmt.Errorf("keyservice/pkcs11: re-OpenSession: %w", oerr)
			}
			return &p11Session{ctx: p.ctx, sh: sh}, nil
		}
		return nil, fmt.Errorf("keyservice/pkcs11: Login: %w", err)
	}
	return &p11Session{ctx: p.ctx, sh: sh}, nil
}

// Close logs out (best-effort) and closes the underlying session
// handle. Errors are intentionally swallowed: cleanup must not mask
// the operation's primary error.
func (s *p11Session) Close() {
	_ = s.ctx.Logout(s.sh)
	_ = s.ctx.CloseSession(s.sh)
}

// findKeyByLabel returns the persistent AES key object handle for the
// given label, or errPKCS11NotFound if no matching object exists.
func (s *p11Session) findKeyByLabel(label string) (pkcs11.ObjectHandle, error) {
	tmpl := []*pkcs11.Attribute{
		pkcs11.NewAttribute(pkcs11.CKA_CLASS, pkcs11.CKO_SECRET_KEY),
		pkcs11.NewAttribute(pkcs11.CKA_KEY_TYPE, pkcs11.CKK_AES),
		pkcs11.NewAttribute(pkcs11.CKA_LABEL, label),
	}
	if err := s.ctx.FindObjectsInit(s.sh, tmpl); err != nil {
		return 0, fmt.Errorf("keyservice/pkcs11: FindObjectsInit(%q): %w", label, err)
	}
	objs, _, err := s.ctx.FindObjects(s.sh, 1)
	_ = s.ctx.FindObjectsFinal(s.sh)
	if err != nil {
		return 0, fmt.Errorf("keyservice/pkcs11: FindObjects(%q): %w", label, err)
	}
	if len(objs) == 0 {
		return 0, errPKCS11NotFound
	}
	return objs[0], nil
}

// generateAESKey creates a 256-bit AES key on the token. The key is
// CKA_EXTRACTABLE=true / CKA_SENSITIVE=false so the recipient-wrap
// code path can read CKA_VALUE for ECIES out-of-HSM. CKA_ID carries
// the GCM nonce piggybacked alongside the key (12 bytes; well within
// PKCS#11's CKA_ID 32-byte commodity bound).
func (s *p11Session) generateAESKey(label string, nonce []byte) (pkcs11.ObjectHandle, error) {
	mech := []*pkcs11.Mechanism{pkcs11.NewMechanism(pkcs11.CKM_AES_KEY_GEN, nil)}
	tmpl := []*pkcs11.Attribute{
		pkcs11.NewAttribute(pkcs11.CKA_CLASS, pkcs11.CKO_SECRET_KEY),
		pkcs11.NewAttribute(pkcs11.CKA_KEY_TYPE, pkcs11.CKK_AES),
		pkcs11.NewAttribute(pkcs11.CKA_VALUE_LEN, 32),
		pkcs11.NewAttribute(pkcs11.CKA_TOKEN, true),
		pkcs11.NewAttribute(pkcs11.CKA_PRIVATE, true),
		pkcs11.NewAttribute(pkcs11.CKA_SENSITIVE, false),
		pkcs11.NewAttribute(pkcs11.CKA_EXTRACTABLE, true),
		pkcs11.NewAttribute(pkcs11.CKA_ENCRYPT, true),
		pkcs11.NewAttribute(pkcs11.CKA_DECRYPT, true),
		pkcs11.NewAttribute(pkcs11.CKA_LABEL, label),
		pkcs11.NewAttribute(pkcs11.CKA_ID, nonce),
	}
	oh, err := s.ctx.GenerateKey(s.sh, mech, tmpl)
	if err != nil {
		return 0, fmt.Errorf("keyservice/pkcs11: GenerateKey(%q): %w", label, err)
	}
	return oh, nil
}

// readKeyMaterial extracts the AES value + GCM nonce from the named
// key object. The value is sensitive: the caller MUST zeroize it.
func (s *p11Session) readKeyMaterial(oh pkcs11.ObjectHandle) (key, nonce []byte, err error) {
	attrs := []*pkcs11.Attribute{
		pkcs11.NewAttribute(pkcs11.CKA_VALUE, nil),
		pkcs11.NewAttribute(pkcs11.CKA_ID, nil),
	}
	got, err := s.ctx.GetAttributeValue(s.sh, oh, attrs)
	if err != nil {
		return nil, nil, fmt.Errorf("keyservice/pkcs11: GetAttributeValue: %w", err)
	}
	for _, a := range got {
		switch a.Type {
		case pkcs11.CKA_VALUE:
			key = append([]byte(nil), a.Value...)
		case pkcs11.CKA_ID:
			nonce = append([]byte(nil), a.Value...)
		}
	}
	if len(key) == 0 || len(nonce) == 0 {
		return nil, nil, errors.New("keyservice/pkcs11: key object missing CKA_VALUE or CKA_ID")
	}
	return key, nonce, nil
}

// gcmEncrypt encrypts plaintext under the named key using
// CKM_AES_GCM. The HSM produces "ciphertext || 16-byte-tag" — the
// same wire layout Go's gcm.Seal(nil, nonce, plaintext, nil) emits,
// which is what crypto/artifact.DecryptArtifact expects.
func (s *p11Session) gcmEncrypt(oh pkcs11.ObjectHandle, nonce, plaintext []byte) ([]byte, error) {
	params := pkcs11.NewGCMParams(nonce, nil, 128)
	defer params.Free()
	mech := []*pkcs11.Mechanism{pkcs11.NewMechanism(pkcs11.CKM_AES_GCM, params)}
	if err := s.ctx.EncryptInit(s.sh, mech, oh); err != nil {
		return nil, fmt.Errorf("keyservice/pkcs11: EncryptInit: %w", err)
	}
	ct, err := s.ctx.Encrypt(s.sh, plaintext)
	if err != nil {
		return nil, fmt.Errorf("keyservice/pkcs11: Encrypt: %w", err)
	}
	return ct, nil
}

// gcmDecrypt decrypts ciphertext under the named key using CKM_AES_GCM.
// The trust boundary stays inside the HSM — the AES key never crosses
// process memory on this path. Used by Decrypt() (the in-process
// AES-GCM open is reserved for key material that has been ECIES-
// extracted on the recipient side, not here).
func (s *p11Session) gcmDecrypt(oh pkcs11.ObjectHandle, nonce, ciphertext []byte) ([]byte, error) {
	params := pkcs11.NewGCMParams(nonce, nil, 128)
	defer params.Free()
	mech := []*pkcs11.Mechanism{pkcs11.NewMechanism(pkcs11.CKM_AES_GCM, params)}
	if err := s.ctx.DecryptInit(s.sh, mech, oh); err != nil {
		return nil, fmt.Errorf("keyservice/pkcs11: DecryptInit: %w", err)
	}
	pt, err := s.ctx.Decrypt(s.sh, ciphertext)
	if err != nil {
		return nil, fmt.Errorf("keyservice/pkcs11: Decrypt: %w", err)
	}
	return pt, nil
}

// destroyKey deletes the persistent key object. Returns
// errPKCS11NotFound if the underlying HSM reports the handle as
// invalid (idempotent caller path).
func (s *p11Session) destroyKey(oh pkcs11.ObjectHandle) error {
	if err := s.ctx.DestroyObject(s.sh, oh); err != nil {
		var pErr pkcs11.Error
		if errors.As(err, &pErr) {
			switch uint(pErr) {
			case pkcs11.CKR_OBJECT_HANDLE_INVALID, pkcs11.CKR_KEY_HANDLE_INVALID:
				return errPKCS11NotFound
			}
		}
		return fmt.Errorf("keyservice/pkcs11: DestroyObject: %w", err)
	}
	return nil
}

// findSlotByTokenLabel scans the present-token slots and returns the
// one whose token label matches cfg.TokenLabel. Returns a friendly
// error if no slot matches — production HSMs are typically pinned to
// a known slot, but SoftHSM2 dev tokens get whatever slot ID the
// daemon assigns at init time.
func findSlotByTokenLabel(ctx *pkcs11.Ctx, label string) (uint, error) {
	slots, err := ctx.GetSlotList(true)
	if err != nil {
		return 0, fmt.Errorf("keyservice/pkcs11: GetSlotList: %w", err)
	}
	for _, sid := range slots {
		ti, err := ctx.GetTokenInfo(sid)
		if err != nil {
			continue
		}
		if trimNul(ti.Label) == label {
			return sid, nil
		}
	}
	return 0, fmt.Errorf("keyservice/pkcs11: no slot with token label %q (have %d slot(s))", label, len(slots))
}

// trimNul strips trailing NUL/space padding the way PKCS#11 fixed-
// width string fields are conventionally rendered (the spec right-
// pads token labels to 32 bytes with 0x20).
func trimNul(s string) string {
	end := len(s)
	for end > 0 && (s[end-1] == 0x00 || s[end-1] == 0x20) {
		end--
	}
	return s[:end]
}
