package signing

// The v2 key file (SIGNING.md, "The key file"): JSON holding a signing key's public half and its 32-byte
// Ed25519 seed encrypted under one or more slots, each a different secret. It holds only ciphertext, so it
// can be copied between apps. Here a slot opens with a passphrase or the recovery code. A passkey slot needs
// a browser, or a FIDO2 key through CTAP2 hmac-secret (HMACSecretSalt).

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/hkdf"
	"golang.org/x/text/unicode/norm"
)

// Keyfile is a v2 signing key file.
type Keyfile struct {
	V       int    `json:"v"`
	Pub     string `json:"pub"`
	Created int64  `json:"created"`
	Slots   []Slot `json:"slots"`
}

// Slot is one way to open a key file.
type Slot struct {
	Type    string `json:"type"` // passkey, passphrase or recovery
	CredID  string `json:"cred_id,omitempty"`
	RPID    string `json:"rp_id,omitempty"`
	PRFSalt string `json:"prf_salt,omitempty"`
	KDF     *struct {
		Alg string `json:"alg"`
		M   uint32 `json:"m"`
		T   uint32 `json:"t"`
		P   uint8  `json:"p"`
	} `json:"kdf,omitempty"`
	KDFSalt string `json:"kdf_salt,omitempty"`
	Salt    string `json:"salt"`
	Nonce   string `json:"nonce"`
	CT      string `json:"ct"`
}

// ErrWrongSecret is a passphrase or recovery code that doesn't open the key file.
var ErrWrongSecret = errors.New("wrong passphrase or recovery code")

// LoadKeyfile reads and checks a key file.
func LoadKeyfile(path string) (Keyfile, error) {
	f, err := os.Open(path)
	if err != nil {
		return Keyfile{}, err
	}
	defer f.Close()
	return ReadKeyfile(io.LimitReader(f, 1<<16))
}

// ReadKeyfile parses and checks a key file.
func ReadKeyfile(r io.Reader) (Keyfile, error) {
	var k Keyfile
	if err := json.NewDecoder(r).Decode(&k); err != nil {
		return k, fmt.Errorf("signing key file: %w", err)
	}
	if k.V != 2 {
		return k, fmt.Errorf("signing key file: version %d, want 2", k.V)
	}
	if pub, err := hex.DecodeString(k.Pub); err != nil || len(pub) != 32 {
		return k, errors.New("signing key file: pub isn't a 32-byte hex key")
	}
	if len(k.Slots) == 0 {
		return k, errors.New("signing key file: no slots")
	}
	return k, nil
}

// PublicKey is the signing key's public half.
func (k Keyfile) PublicKey() [32]byte {
	var p [32]byte
	b, _ := hex.DecodeString(k.Pub)
	copy(p[:], b)
	return p
}

// Has reports whether the key file has a slot of a type.
func (k Keyfile) Has(typ string) bool {
	for _, s := range k.Slots {
		if s.Type == typ {
			return true
		}
	}
	return false
}

// UnlockPassphrase opens the key file's passphrase slot. The seed it returns should be wiped (Wipe) once used.
func (k Keyfile) UnlockPassphrase(passphrase string) (ed25519.PrivateKey, error) {
	for _, s := range k.Slots {
		if s.Type != "passphrase" || s.KDF == nil || s.KDF.Alg != "argon2id" {
			continue
		}
		salt, err := unb64(s.KDFSalt)
		if err != nil {
			return nil, err
		}
		if s.KDF.M > 1<<21 || s.KDF.T > 64 || s.KDF.P == 0 { // refuse a file that would take minutes or gigabytes
			return nil, errors.New("signing key file: passphrase settings out of range")
		}
		ikm := argon2.IDKey([]byte(norm.NFKC.String(passphrase)), salt, s.KDF.T, s.KDF.M, s.KDF.P, 32)
		return k.open(s, ikm)
	}
	return nil, errors.New("this key file has no passphrase: add one in the app that made it")
}

// UnlockRecovery opens the recovery slot with the recovery code, as shown when the key was made.
func (k Keyfile) UnlockRecovery(code string) (ed25519.PrivateKey, error) {
	ikm, err := RecoveryBytes(code)
	if err != nil {
		return nil, err
	}
	for _, s := range k.Slots {
		if s.Type == "recovery" {
			return k.open(s, ikm)
		}
	}
	return nil, errors.New("this key file has no recovery slot")
}

// UnlockPRF opens a passkey slot with the 32 bytes its authenticator returned (WebAuthn PRF, or CTAP2
// hmac-secret over HMACSecretSalt).
func (k Keyfile) UnlockPRF(s Slot, prf []byte) (ed25519.PrivateKey, error) {
	if s.Type != "passkey" || len(prf) != 32 {
		return nil, errors.New("not a passkey slot, or not a 32-byte secret")
	}
	return k.open(s, prf)
}

// HMACSecretSalt is the CTAP2 hmac-secret salt that gives the same secret as a browser's WebAuthn PRF for a
// passkey slot, for opening one with a FIDO2 key outside a browser.
func HMACSecretSalt(s Slot) ([32]byte, error) {
	salt, err := unb64(s.PRFSalt)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(append([]byte("WebAuthn PRF\x00"), salt...)), nil
}

func (k Keyfile) open(s Slot, ikm []byte) (ed25519.PrivateKey, error) {
	salt, err1 := unb64(s.Salt)
	nonce, err2 := unb64(s.Nonce)
	ct, err3 := unb64(s.CT)
	if err := errors.Join(err1, err2, err3); err != nil {
		return nil, fmt.Errorf("signing key file: %w", err)
	}
	kek := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, ikm, salt, []byte("mc-companion-slot-v2/"+s.Type)), kek); err != nil {
		return nil, err
	}
	block, _ := aes.NewCipher(kek)
	gcm, _ := cipher.NewGCM(block)
	if len(nonce) != gcm.NonceSize() {
		return nil, errors.New("signing key file: bad nonce")
	}
	pub := k.PublicKey()
	seed, err := gcm.Open(nil, nonce, ct, append([]byte("mc-keyfile-v2"), pub[:]...))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, ErrWrongSecret
	}
	priv := ed25519.NewKeyFromSeed(seed)
	Wipe(seed)
	if !bytes32Equal(priv.Public().(ed25519.PublicKey), pub) {
		Wipe(priv)
		return nil, errors.New("signing key file: the key inside doesn't match its public key")
	}
	return priv, nil
}

// Wipe zeroes a key once it's been used.
func Wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// RecoveryBytes reads a recovery code: 32 bytes as base32 groups (ABCDEFGHJKLMNPQRSTUVWXYZ23456789).
func RecoveryBytes(code string) ([]byte, error) {
	const alpha = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	var bits strings.Builder
	for _, c := range strings.ToUpper(code) {
		if !unicode.IsLetter(c) && !unicode.IsDigit(c) {
			continue
		}
		i := strings.IndexRune(alpha, c)
		if i < 0 {
			return nil, errors.New("that isn't a recovery code")
		}
		fmt.Fprintf(&bits, "%05b", i)
	}
	s := bits.String()
	if len(s) < 256 {
		return nil, errors.New("that recovery code is too short")
	}
	out := make([]byte, 32)
	for i := range out {
		var v byte
		for _, c := range s[i*8 : i*8+8] {
			v = v<<1 | byte(c-'0')
		}
		out[i] = v
	}
	return out, nil
}

func unb64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

func bytes32Equal(a []byte, b [32]byte) bool { return len(a) == 32 && string(a) == string(b[:]) }
