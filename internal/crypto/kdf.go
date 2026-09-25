package crypto

import (
	"crypto/rand"
	"errors"
	"fmt"

	"filippo.io/age"
	"golang.org/x/crypto/argon2"
)

// KDFParams says how the key is derived from a passphrase. It is stored
// unencrypted next to the synced data: a salt is not secret, and every
// machine needs the same salt to derive the same key.
type KDFParams struct {
	Version   int    `json:"version"`
	Algorithm string `json:"algorithm"`
	Salt      []byte `json:"salt"`
	Time      uint32 `json:"time"`
	MemoryKiB uint32 `json:"memory_kib"`
	Threads   uint8  `json:"threads"`
}

const kdfAlgorithm = "argon2id"

// Bounds on parameters read from storage, so a tampered kdf.json cannot make
// a machine allocate gigabytes or spin for minutes.
const (
	minSaltLen   = 16
	maxTime      = 10
	maxMemoryKiB = 1 << 20 // 1 GiB
	maxThreads   = 16
)

// NewKDFParams returns the current parameters with a fresh random salt.
func NewKDFParams() (KDFParams, error) {
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return KDFParams{}, fmt.Errorf("generate salt: %w", err)
	}
	return KDFParams{
		Version:   1,
		Algorithm: kdfAlgorithm,
		Salt:      salt,
		Time:      3,
		MemoryKiB: 64 * 1024,
		Threads:   4,
	}, nil
}

// Validate rejects parameters this version cannot use or that are out of
// bounds.
func (p KDFParams) Validate() error {
	switch {
	case p.Version != 1:
		return fmt.Errorf("unsupported key derivation version %d", p.Version)
	case p.Algorithm != kdfAlgorithm:
		return fmt.Errorf("unsupported key derivation algorithm %q", p.Algorithm)
	case len(p.Salt) < minSaltLen:
		return fmt.Errorf("key derivation salt is too short (%d bytes)", len(p.Salt))
	case p.Time < 1 || p.Time > maxTime:
		return fmt.Errorf("key derivation time %d is out of range", p.Time)
	case p.MemoryKiB < 8*1024 || p.MemoryKiB > maxMemoryKiB:
		return fmt.Errorf("key derivation memory %d KiB is out of range", p.MemoryKiB)
	case p.Threads < 1 || p.Threads > maxThreads:
		return fmt.Errorf("key derivation threads %d is out of range", p.Threads)
	}
	return nil
}

// DeriveIdentity derives the age identity for passphrase under p. The same
// passphrase and parameters give the same identity on every machine.
func DeriveIdentity(passphrase string, p KDFParams) (*age.X25519Identity, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if passphrase == "" {
		return nil, errors.New("passphrase is empty")
	}
	scalar := argon2.IDKey([]byte(passphrase), p.Salt, p.Time, p.MemoryKiB, p.Threads, 32)
	return identityFromScalar(scalar)
}

// identityFromScalar turns 32 bytes of key material into an age identity.
func identityFromScalar(scalar []byte) (*age.X25519Identity, error) {
	// Clamp the scalar for X25519 (RFC 7748).
	scalar[0] &= 248
	scalar[31] &= 127
	scalar[31] |= 64
	encoded, err := encodeAgeIdentity(scalar)
	if err != nil {
		return nil, err
	}
	return age.ParseX25519Identity(encoded)
}

// NewEncryptorFromIdentity returns an Encryptor for an identity already in
// memory, such as one derived from a passphrase.
func NewEncryptorFromIdentity(id *age.X25519Identity) *Encryptor {
	return &Encryptor{identity: id, recipient: id.Recipient()}
}

// Identity returns the identity in age's text form, for saving to a key file.
func (e *Encryptor) Identity() string {
	return e.identity.String()
}
