package crypto

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
)

func TestGenerateKey(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "age-key.txt")

	err := GenerateKey(keyPath)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	// Verify key file exists
	if !KeyExists(keyPath) {
		t.Fatal("Key file was not created")
	}

	// Read and validate the key
	data, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("Failed to read key file: %v", err)
	}

	keyStr := strings.TrimSpace(string(data))

	// Should start with AGE-SECRET-KEY-
	if !strings.HasPrefix(keyStr, "AGE-SECRET-KEY-") {
		t.Errorf("Key should start with AGE-SECRET-KEY-, got: %s", keyStr[:20])
	}

	// Key should be parseable by age library
	_, err = age.ParseX25519Identity(keyStr)
	if err != nil {
		t.Errorf("Generated key is not valid age identity: %v", err)
	}
}

func TestNewEncryptor(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "age-key.txt")

	// Generate a key first
	err := GenerateKey(keyPath)
	if err != nil {
		t.Fatalf("Failed to generate key: %v", err)
	}

	// Create encryptor
	enc, err := NewEncryptor(keyPath)
	if err != nil {
		t.Fatalf("NewEncryptor failed: %v", err)
	}

	if enc == nil {
		t.Fatal("Encryptor is nil")
	}

	// Verify public key is not empty
	pubKey := enc.PublicKey()
	if pubKey == "" {
		t.Error("Public key is empty")
	}
	if !strings.HasPrefix(pubKey, "age1") {
		t.Errorf("Public key should start with age1, got: %s", pubKey)
	}
}

func TestEncryptDecrypt(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "age-key.txt")

	err := GenerateKey(keyPath)
	if err != nil {
		t.Fatalf("Failed to generate key: %v", err)
	}

	enc, err := NewEncryptor(keyPath)
	if err != nil {
		t.Fatalf("NewEncryptor failed: %v", err)
	}

	// Test encryption/decryption
	plaintext := []byte("Hello, World! This is a test message for encryption.")

	ciphertext, err := enc.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	// Ciphertext should be different from plaintext
	if string(ciphertext) == string(plaintext) {
		t.Error("Ciphertext should not equal plaintext")
	}

	// Decrypt
	decrypted, err := enc.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}

	// Decrypted should match original
	if string(decrypted) != string(plaintext) {
		t.Errorf("Decrypted text doesn't match original.\nExpected: %s\nGot: %s", plaintext, decrypted)
	}
}

func TestEncryptDecryptLargeData(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "age-key.txt")

	err := GenerateKey(keyPath)
	if err != nil {
		t.Fatalf("Failed to generate key: %v", err)
	}

	enc, err := NewEncryptor(keyPath)
	if err != nil {
		t.Fatalf("NewEncryptor failed: %v", err)
	}

	// Create 1MB of test data
	plaintext := make([]byte, 1024*1024)
	for i := range plaintext {
		plaintext[i] = byte(i % 256)
	}

	ciphertext, err := enc.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt large data failed: %v", err)
	}

	decrypted, err := enc.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt large data failed: %v", err)
	}

	if len(decrypted) != len(plaintext) {
		t.Errorf("Decrypted length mismatch: expected %d, got %d", len(plaintext), len(decrypted))
	}

	for i := range plaintext {
		if decrypted[i] != plaintext[i] {
			t.Errorf("Decrypted data mismatch at byte %d", i)
			break
		}
	}
}

func TestValidatePassphraseStrength(t *testing.T) {
	tests := []struct {
		passphrase string
		wantErr    bool
	}{
		{"short", true},         // Too short (< 12)
		{"1234567", true},       // Still too short (7 chars)
		{"12345678", true},      // Still too short (8 chars, now requires 12)
		{"12345678901", true},   // Still too short (11 chars)
		{"123456789012", false}, // Minimum length (12)
		{"longenoughpass", false},
		{"very-long-passphrase-that-is-secure", false},
	}

	for _, tt := range tests {
		t.Run(tt.passphrase, func(t *testing.T) {
			err := ValidatePassphraseStrength(tt.passphrase)
			if tt.wantErr && err == nil {
				t.Errorf("Expected error for passphrase %q, got nil", tt.passphrase)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Unexpected error for passphrase %q: %v", tt.passphrase, err)
			}
		})
	}
}

func TestKeyExists(t *testing.T) {
	tmpDir := t.TempDir()

	// Non-existent key
	keyPath := filepath.Join(tmpDir, "nonexistent.txt")
	if KeyExists(keyPath) {
		t.Error("KeyExists should return false for non-existent file")
	}

	// Create a key
	err := GenerateKey(keyPath)
	if err != nil {
		t.Fatalf("Failed to generate key: %v", err)
	}

	// Now it should exist
	if !KeyExists(keyPath) {
		t.Error("KeyExists should return true after key is created")
	}
}

func TestEncodeAgeIdentity(t *testing.T) {
	// Test that encoded identity is all uppercase
	scalar := make([]byte, 32)
	for i := range scalar {
		scalar[i] = byte(i * 7 % 256)
	}

	// Clamp the scalar
	scalar[0] &= 248
	scalar[31] &= 127
	scalar[31] |= 64

	identity, err := encodeAgeIdentity(scalar)
	if err != nil {
		t.Fatalf("encodeAgeIdentity failed: %v", err)
	}

	// Check prefix
	if !strings.HasPrefix(identity, "AGE-SECRET-KEY-1") {
		t.Errorf("Identity should start with AGE-SECRET-KEY-1, got: %s", identity[:20])
	}

	// Check no lowercase after the separator
	separatorIdx := strings.Index(identity, "1")
	if separatorIdx > 0 {
		dataPart := identity[separatorIdx+1:]
		for _, c := range dataPart {
			if c >= 'a' && c <= 'z' {
				t.Errorf("Identity contains lowercase after separator: %s", identity)
				break
			}
		}
	}

	// Verify it's parseable by age
	_, err = age.ParseX25519Identity(identity)
	if err != nil {
		t.Errorf("Encoded identity is not valid: %v", err)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
