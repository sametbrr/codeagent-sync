package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"filippo.io/age"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/sametbrr/codeagent-sync/internal/config"
	"github.com/sametbrr/codeagent-sync/internal/crypto"
	"github.com/sametbrr/codeagent-sync/internal/platform"
	"github.com/sametbrr/codeagent-sync/internal/storage"
)

// joinPrefix starts a join code.
const joinPrefix = "cas1-"

// joinPayload is what a join code carries, encrypted with a passphrase.
type joinPayload struct {
	Version int                   `yaml:"version"`
	Storage storage.StorageConfig `yaml:"storage"`
	// Key is the bucket's age identity when the bucket uses a key file; a
	// passphrase bucket derives it from the same passphrase instead.
	Key string `yaml:"key,omitempty"`
}

func (a *app) joinCodeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "join-code",
		Short: "Print a code that sets up another machine with this machine's storage",
		Long: `Print a code that sets up another machine: run codeagent-sync init --join <code>
there. The code carries the storage settings and credentials, encrypted with
the bucket's passphrase (or, for a bucket that uses a key file, the key and a
passphrase you choose), so it is safe to pass through a chat with yourself.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			code, err := a.makeJoinCode(runContext(cmd))
			if err != nil {
				return err
			}
			if a.jsonOut {
				return json.NewEncoder(a.out).Encode(map[string]string{"code": code})
			}
			a.printf("%s\n\n", code)
			a.info("On the other machine run: codeagent-sync init --join <this code>")
			a.info("It asks for the passphrase the code is encrypted with.")
			return nil
		},
	}
}

func (a *app) makeJoinCode(ctx context.Context) (string, error) {
	dirs, err := platform.LocalDirs()
	if err != nil {
		return "", err
	}
	cfg, err := config.Load(dirs.State)
	if err != nil {
		return "", err
	}
	id, err := crypto.LoadIdentity(cfg.KeyPath(dirs.Home))
	if err != nil {
		return "", err
	}
	payload := joinPayload{Version: 1, Storage: cfg.Storage}
	if st := &payload.Storage; st.CredentialsFile != "" {
		data, err := os.ReadFile(config.ExpandHome(st.CredentialsFile, dirs.Home))
		if err != nil {
			return "", fmt.Errorf("read the GCS credentials: %w", err)
		}
		st.CredentialsJSON, st.CredentialsFile = string(data), ""
	}

	store, err := storage.New(&cfg.Storage)
	if err != nil {
		return "", err
	}
	var pass string
	data, _, err := store.Get(ctx, kdfKey)
	switch {
	case err == nil:
		var params crypto.KDFParams
		if err := json.Unmarshal(data, &params); err != nil {
			return "", fmt.Errorf("%s is damaged: %w", kdfKey, err)
		}
		if pass, err = a.askPassphrase("Passphrase of the bucket:"); err != nil {
			return "", err
		}
		derived, err := crypto.DeriveIdentity(pass, params)
		if err != nil {
			return "", err
		}
		if derived.String() != id.String() {
			return "", errors.New("that is not the bucket's passphrase")
		}
	case errors.Is(err, storage.ErrNotFound):
		a.info("This bucket uses a key file; the code carries it, encrypted with a passphrase you choose now.")
		if pass, err = a.newPassphrase(); err != nil {
			return "", err
		}
		payload.Key = id.String()
	default:
		return "", err
	}

	plain, err := yaml.Marshal(payload)
	if err != nil {
		return "", err
	}
	recipient, err := age.NewScryptRecipient(pass)
	if err != nil {
		return "", err
	}
	var sealed bytes.Buffer
	w, err := age.Encrypt(&sealed, recipient)
	if err != nil {
		return "", err
	}
	if _, err := w.Write(plain); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return joinPrefix + base64.RawURLEncoding.EncodeToString(sealed.Bytes()), nil
}

// openJoinCode decrypts a join code with a passphrase, which it keeps for
// the key setup that follows.
func (a *app) openJoinCode(code string) (*joinPayload, error) {
	code = strings.TrimSpace(code)
	if !strings.HasPrefix(code, joinPrefix) {
		return nil, fmt.Errorf("a join code starts with %s", joinPrefix)
	}
	sealed, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(code, joinPrefix))
	if err != nil {
		return nil, errors.New("the join code is damaged (was it copied whole?)")
	}
	for attempt := 1; ; attempt++ {
		pass, err := a.askPassphrase("Passphrase:")
		if err != nil {
			return nil, err
		}
		identity, err := age.NewScryptIdentity(pass)
		if err != nil {
			return nil, err
		}
		r, err := age.Decrypt(bytes.NewReader(sealed), identity)
		if err != nil {
			if os.Getenv(passphraseEnv) != "" || attempt == 3 {
				return nil, errors.New("that passphrase does not open the join code")
			}
			a.warn("That passphrase does not open the join code; try again.")
			continue
		}
		plain, err := io.ReadAll(io.LimitReader(r, 1<<20))
		if err != nil {
			return nil, err
		}
		var p joinPayload
		if err := yaml.Unmarshal(plain, &p); err != nil || p.Version != 1 {
			return nil, errors.New("the join code comes from a different version of codeagent-sync")
		}
		a.passphrase = pass
		return &p, p.Storage.Validate()
	}
}
