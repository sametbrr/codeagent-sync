package crypto

import (
	"bytes"
	"errors"
	"testing"
)

func TestNewKDFParamsAreValidAndSalted(t *testing.T) {
	a, err := NewKDFParams()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewKDFParams()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Validate(); err != nil {
		t.Errorf("fresh params are invalid: %v", err)
	}
	if bytes.Equal(a.Salt, b.Salt) {
		t.Error("two fresh parameter sets share a salt")
	}
}

func TestDeriveIdentityDependsOnPassphraseAndSalt(t *testing.T) {
	p, _ := NewKDFParams()
	other, _ := NewKDFParams()

	id1, err := DeriveIdentity("correct horse battery staple", p)
	if err != nil {
		t.Fatal(err)
	}
	id2, _ := DeriveIdentity("correct horse battery staple", p)
	if id1.String() != id2.String() {
		t.Error("the same passphrase and salt gave different identities")
	}
	if id3, _ := DeriveIdentity("correct horse battery staple", other); id3.String() == id1.String() {
		t.Error("a different salt gave the same identity")
	}
	if id4, _ := DeriveIdentity("another passphrase entirely", p); id4.String() == id1.String() {
		t.Error("a different passphrase gave the same identity")
	}
}

// Two machines deriving from the same stored parameters can read each
// other's data.
func TestDerivedIdentityDecryptsAcrossMachines(t *testing.T) {
	p, _ := NewKDFParams()
	idA, _ := DeriveIdentity("shared passphrase 123", p)
	idB, _ := DeriveIdentity("shared passphrase 123", p)

	ciphertext, err := NewEncryptorFromIdentity(idA).Encrypt([]byte("settings"))
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := NewEncryptorFromIdentity(idB).Decrypt(ciphertext)
	if err != nil || string(plaintext) != "settings" {
		t.Errorf("machine B read %q, %v", plaintext, err)
	}
}

func TestValidateRejectsHostileParams(t *testing.T) {
	good, _ := NewKDFParams()
	tests := map[string]func(*KDFParams){
		"version":     func(p *KDFParams) { p.Version = 2 },
		"algorithm":   func(p *KDFParams) { p.Algorithm = "scrypt" },
		"short salt":  func(p *KDFParams) { p.Salt = p.Salt[:8] },
		"zero time":   func(p *KDFParams) { p.Time = 0 },
		"huge time":   func(p *KDFParams) { p.Time = 1000 },
		"tiny memory": func(p *KDFParams) { p.MemoryKiB = 1 },
		"huge memory": func(p *KDFParams) { p.MemoryKiB = 64 << 20 },
		"no threads":  func(p *KDFParams) { p.Threads = 0 },
	}
	for name, mutate := range tests {
		p := good
		p.Salt = append([]byte(nil), good.Salt...)
		mutate(&p)
		if err := p.Validate(); err == nil {
			t.Errorf("%s: Validate accepted %+v", name, p)
		}
		if _, err := DeriveIdentity("some passphrase", p); err == nil {
			t.Errorf("%s: DeriveIdentity accepted invalid params", name)
		}
	}
}

func TestKeyCheck(t *testing.T) {
	p, _ := NewKDFParams()
	right, _ := DeriveIdentity("the right passphrase", p)
	wrong, _ := DeriveIdentity("the wrong passphrase", p)

	check, err := MakeKeyCheck(NewEncryptorFromIdentity(right))
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyKeyCheck(NewEncryptorFromIdentity(right), check); err != nil {
		t.Errorf("right key: %v", err)
	}
	if err := VerifyKeyCheck(NewEncryptorFromIdentity(wrong), check); !errors.Is(err, ErrWrongKey) {
		t.Errorf("wrong key: %v, want ErrWrongKey", err)
	}
	if err := VerifyKeyCheck(NewEncryptorFromIdentity(right), []byte("garbage")); !errors.Is(err, ErrWrongKey) {
		t.Errorf("corrupt check: %v, want ErrWrongKey", err)
	}
}
