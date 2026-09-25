package crypto

import (
	"bytes"
	"errors"
)

// keyCheckPlaintext is what the key check object decrypts to.
var keyCheckPlaintext = []byte("codeagent-sync key check v1")

// ErrWrongKey means the key or passphrase is not the one the data was
// encrypted with.
var ErrWrongKey = errors.New("the key does not match the synced data (wrong passphrase or key file)")

// MakeKeyCheck returns a small ciphertext that is stored with the synced
// data. Decrypting it proves that a key is the right one before any file is
// downloaded or written.
func MakeKeyCheck(e *Encryptor) ([]byte, error) {
	return e.Encrypt(keyCheckPlaintext)
}

// VerifyKeyCheck reports ErrWrongKey unless e decrypts the key check.
func VerifyKeyCheck(e *Encryptor, check []byte) error {
	plaintext, err := e.Decrypt(check)
	if err != nil || !bytes.Equal(plaintext, keyCheckPlaintext) {
		return ErrWrongKey
	}
	return nil
}
