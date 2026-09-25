package crypto

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"filippo.io/age"
	"github.com/btcsuite/btcd/btcutil/bech32"
)

type Encryptor struct {
	identity  *age.X25519Identity
	recipient *age.X25519Recipient
}

func NewEncryptor(keyPath string) (*Encryptor, error) {
	identity, err := LoadIdentity(keyPath)
	if err != nil {
		return nil, err
	}
	return NewEncryptorFromIdentity(identity), nil
}

// LoadIdentity reads an age identity (private key) from a key file.
func LoadIdentity(keyPath string) (*age.X25519Identity, error) {
	data, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read age key: %w", err)
	}
	identity, err := age.ParseX25519Identity(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, fmt.Errorf("failed to parse age identity in %s: %w", keyPath, err)
	}
	return identity, nil
}

func (e *Encryptor) Encrypt(plaintext []byte) ([]byte, error) {
	var buf bytes.Buffer

	w, err := age.Encrypt(&buf, e.recipient)
	if err != nil {
		return nil, fmt.Errorf("failed to create encryption writer: %w", err)
	}

	if _, err := w.Write(plaintext); err != nil {
		return nil, fmt.Errorf("failed to write encrypted data: %w", err)
	}

	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("failed to finalize encryption: %w", err)
	}

	return buf.Bytes(), nil
}

func (e *Encryptor) Decrypt(ciphertext []byte) ([]byte, error) {
	r, err := age.Decrypt(bytes.NewReader(ciphertext), e.identity)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt: %w", err)
	}

	plaintext, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("failed to read decrypted data: %w", err)
	}

	return plaintext, nil
}

func (e *Encryptor) PublicKey() string {
	return e.recipient.String()
}

func GenerateKey(keyPath string) error {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		return fmt.Errorf("failed to generate age key: %w", err)
	}

	if err := os.WriteFile(keyPath, []byte(identity.String()+"\n"), 0600); err != nil {
		return fmt.Errorf("failed to write age key: %w", err)
	}

	return nil
}

// encodeAgeIdentity encodes a 32-byte scalar as an age identity string
func encodeAgeIdentity(scalar []byte) (string, error) {
	// age uses Bech32 encoding with HRP "age-secret-key-"
	// The bech32 library works with lowercase, then we convert to uppercase
	hrp := "age-secret-key-"

	// Convert 8-bit bytes to 5-bit groups using the bech32 library
	converted, err := bech32.ConvertBits(scalar, 8, 5, true)
	if err != nil {
		return "", fmt.Errorf("failed to convert bits for age identity: %w", err)
	}

	// Encode using bech32
	encoded, err := bech32.Encode(hrp, converted)
	if err != nil {
		return "", fmt.Errorf("failed to bech32 encode age identity: %w", err)
	}

	// Age uses uppercase for secret keys
	return strings.ToUpper(encoded), nil
}

// ValidatePassphraseStrength checks if a passphrase is strong enough.
// Minimum 12 characters required - the fixed Argon2 salt means weak
// passphrases are more vulnerable to brute-force attacks.
func ValidatePassphraseStrength(passphrase string) error {
	if len(passphrase) < 12 {
		return fmt.Errorf("passphrase must be at least 12 characters (got %d)", len(passphrase))
	}
	return nil
}

func KeyExists(keyPath string) bool {
	_, err := os.Stat(keyPath)
	return err == nil
}
