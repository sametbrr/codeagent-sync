package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"filippo.io/age"
	"github.com/AlecAivazis/survey/v2"

	"github.com/sametbrr/codeagent-sync/internal/crypto"
	"github.com/sametbrr/codeagent-sync/internal/entry"
	"github.com/sametbrr/codeagent-sync/internal/storage"
)

// Objects next to the entries that describe how the bucket is encrypted.
const (
	kdfKey   = entry.MetaPrefix + "kdf.json"     // passphrase salt and parameters (not secret)
	checkKey = entry.MetaPrefix + "keycheck.age" // proves a key is the bucket's key
)

// passphraseEnv supplies the passphrase without a prompt (scripts, tests).
const passphraseEnv = "CODEAGENT_SYNC_PASSPHRASE"

// setupKey finds or creates the bucket's encryption key. With a key file it
// uses that identity; otherwise a passphrase derives it. joined reports
// whether the bucket was already set up by another machine.
func (a *app) setupKey(ctx context.Context, store storage.ObjectStore, keyFile string) (id *age.X25519Identity, joined bool, err error) {
	if keyFile != "" {
		if id, err = crypto.LoadIdentity(keyFile); err != nil {
			return nil, false, err
		}
		joined, err = ensureKeyCheck(ctx, store, crypto.NewEncryptorFromIdentity(id))
		return id, joined, err
	}

	data, _, err := store.Get(ctx, kdfKey)
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return a.createPassphraseKey(ctx, store)
	case err != nil:
		return nil, false, err
	}

	var params crypto.KDFParams
	if err := json.Unmarshal(data, &params); err != nil {
		return nil, false, fmt.Errorf("%s is damaged: %w", kdfKey, err)
	}
	check, _, err := store.Get(ctx, checkKey)
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", checkKey, err)
	}
	if a.passphrase == "" {
		a.info("This bucket is already set up; enter its passphrase.")
	}
	for attempt := 1; ; attempt++ {
		pass, err := a.askPassphrase("Passphrase:")
		if err != nil {
			return nil, true, err
		}
		id, err := crypto.DeriveIdentity(pass, params)
		if err != nil {
			return nil, true, err
		}
		if err := crypto.VerifyKeyCheck(crypto.NewEncryptorFromIdentity(id), check); err == nil {
			return id, true, nil
		}
		if os.Getenv(passphraseEnv) != "" || attempt == 3 {
			return nil, true, crypto.ErrWrongKey
		}
		a.warn("That passphrase does not match this bucket; try again.")
	}
}

func (a *app) createPassphraseKey(ctx context.Context, store storage.ObjectStore) (*age.X25519Identity, bool, error) {
	if _, _, err := store.Get(ctx, checkKey); err == nil {
		return nil, false, errors.New("this bucket is encrypted with a key file, not a passphrase; run init with --key-file")
	}
	pass, err := a.newPassphrase()
	if err != nil {
		return nil, false, err
	}
	params, err := crypto.NewKDFParams()
	if err != nil {
		return nil, false, err
	}
	id, err := crypto.DeriveIdentity(pass, params)
	if err != nil {
		return nil, false, err
	}
	raw, err := json.MarshalIndent(params, "", "  ")
	if err != nil {
		return nil, false, err
	}
	_, err = store.Put(ctx, kdfKey, raw, storage.Precondition{IfNoneMatch: true})
	if errors.Is(err, storage.ErrPreconditionFailed) {
		return nil, false, errors.New("another machine set this bucket up at the same moment; run init again to join it")
	}
	if err != nil {
		return nil, false, err
	}
	if _, err := ensureKeyCheck(ctx, store, crypto.NewEncryptorFromIdentity(id)); err != nil {
		return nil, false, err
	}
	return id, false, nil
}

// ensureKeyCheck verifies enc against the bucket's key check, creating the
// check if the bucket has none yet.
func ensureKeyCheck(ctx context.Context, store storage.ObjectStore, enc *crypto.Encryptor) (existed bool, err error) {
	check, _, err := store.Get(ctx, checkKey)
	if errors.Is(err, storage.ErrNotFound) {
		blob, err := crypto.MakeKeyCheck(enc)
		if err != nil {
			return false, err
		}
		if _, err = store.Put(ctx, checkKey, blob, storage.Precondition{IfNoneMatch: true}); !errors.Is(err, storage.ErrPreconditionFailed) {
			return false, err
		}
		if check, _, err = store.Get(ctx, checkKey); err != nil {
			return false, err
		}
	} else if err != nil {
		return false, err
	}
	return true, crypto.VerifyKeyCheck(enc, check)
}

func (a *app) newPassphrase() (string, error) {
	if p := os.Getenv(passphraseEnv); p != "" {
		return p, crypto.ValidatePassphraseStrength(p)
	}
	if !a.interactive() {
		return "", fmt.Errorf("set %s or run init in a terminal to choose a passphrase", passphraseEnv)
	}
	a.info("Choose a passphrase for the bucket. You type it once on each machine;")
	a.info("it cannot be recovered, so keep it in your password manager.")
	for {
		var first, second string
		if err := survey.AskOne(&survey.Password{Message: "New passphrase (at least 12 characters):"}, &first); err != nil {
			return "", err
		}
		if err := crypto.ValidatePassphraseStrength(first); err != nil {
			a.warn("%v", err)
			continue
		}
		if err := survey.AskOne(&survey.Password{Message: "Once more:"}, &second); err != nil {
			return "", err
		}
		if first != second {
			a.warn("The two entries differ; try again.")
			continue
		}
		return first, nil
	}
}

func (a *app) askPassphrase(message string) (string, error) {
	if p := a.passphrase; p != "" {
		a.passphrase = "" // once: a wrong one is asked for again
		return p, nil
	}
	if p := os.Getenv(passphraseEnv); p != "" {
		return p, nil
	}
	if !a.interactive() {
		return "", fmt.Errorf("set %s or run init in a terminal to enter the passphrase", passphraseEnv)
	}
	var p string
	err := survey.AskOne(&survey.Password{Message: message}, &p)
	return p, err
}
