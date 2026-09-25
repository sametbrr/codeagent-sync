package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sametbrr/codeagent-sync/internal/config"
	"github.com/sametbrr/codeagent-sync/internal/crypto"
	"github.com/sametbrr/codeagent-sync/internal/engine"
	"github.com/sametbrr/codeagent-sync/internal/entry"
	"github.com/sametbrr/codeagent-sync/internal/platform"
	"github.com/sametbrr/codeagent-sync/internal/storage"
)

type initOptions struct {
	storage    storage.StorageConfig // from flags; Provider empty = ask
	bucket     string
	keyFile    string
	join       string // a join code from another machine
	yes, force bool
}

func (a *app) initCmd() *cobra.Command {
	var o initOptions
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up this machine: storage, encryption and the first sync",
		Long: `Set up this machine. The first machine chooses the storage and a passphrase;
every other machine points at the same bucket and enters the same passphrase.

Storage settings come from a join code (codeagent-sync join-code on a machine
that is set up), from flags, or from questions. The passphrase can also come from ` + passphraseEnv + `.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runInit(runContext(cmd), o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.bucket, "bucket", "codeagent-sync", "bucket (or WebDAV folder) to use")
	f.StringVar((*string)(&o.storage.Provider), "provider", "", "r2, s3, gcs or webdav (asks when empty)")
	f.StringVar(&o.storage.AccountID, "account-id", "", "R2 account ID")
	f.StringVar(&o.storage.AccessKeyID, "access-key-id", "", "R2/S3 access key ID")
	f.StringVar(&o.storage.SecretAccessKey, "secret-access-key", "", "R2/S3 secret access key")
	f.StringVar(&o.storage.Region, "region", "", "S3 region")
	f.StringVar(&o.storage.Endpoint, "endpoint", "", "S3-compatible endpoint URL")
	f.BoolVar(&o.storage.UsePathStyle, "path-style", false, "S3 path-style addressing (MinIO, Ceph)")
	f.StringVar(&o.storage.ProjectID, "project-id", "", "GCS project ID")
	f.StringVar(&o.storage.CredentialsFile, "credentials-file", "", "GCS credentials file")
	f.StringVar(&o.storage.WebDAVURL, "webdav-url", "", "WebDAV URL")
	f.StringVar(&o.storage.WebDAVUsername, "webdav-user", "", "WebDAV user name")
	f.StringVar(&o.storage.WebDAVPassword, "webdav-password", "", "WebDAV password")
	f.StringVar(&o.keyFile, "key-file", "", "encrypt with this age key file instead of a passphrase")
	f.StringVar(&o.join, "join", "", "join with a code from codeagent-sync join-code on another machine")
	f.BoolVarP(&o.yes, "yes", "y", false, "answer yes to every question")
	f.BoolVar(&o.force, "force", false, "set up again even if this machine is already set up")
	return cmd
}

func (a *app) runInit(ctx context.Context, o initOptions) error {
	dirs, err := platform.LocalDirs()
	if err != nil {
		return err
	}
	if _, err := config.Load(dirs.State); err == nil && !o.force {
		return fmt.Errorf("this machine is already set up (%s); use --force to set it up again", config.Path(dirs.State))
	}

	var st *storage.StorageConfig
	if o.join != "" {
		if o.storage.Provider != "" || o.keyFile != "" {
			return errors.New("a join code carries the storage settings and key; leave out the other flags")
		}
		code, err := a.openJoinCode(o.join)
		if err != nil {
			return err
		}
		st = &code.Storage
		if code.Key != "" {
			o.keyFile = filepath.Join(dirs.State, config.KeyFileName)
			if err := os.MkdirAll(dirs.State, 0o700); err != nil {
				return err
			}
			if err := platform.WriteFileAtomic(o.keyFile, []byte(code.Key+"\n"), 0o600); err != nil {
				return err
			}
		}
	} else if st, err = a.chooseStorage(o, dirs); err != nil {
		return err
	}
	store, err := storage.New(st)
	if err != nil {
		return err
	}
	if err := a.ensureBucket(ctx, store, st, o.yes); err != nil {
		return err
	}

	probe, err := storage.Probe(ctx, store, entry.MetaPrefix+"probe/")
	if err != nil {
		return fmt.Errorf("the storage does not accept writes: %w", err)
	}
	conditional := probe.ConditionalWrites()
	if !conditional {
		a.warn("This storage ignores conditional writes: if two machines sync at the same")
		a.warn("moment, one can overwrite the other's change. Sync one machine at a time.")
	}

	id, joined, err := a.setupKey(ctx, store, o.keyFile)
	if err != nil {
		return err
	}
	keyPath := filepath.Join(dirs.State, config.KeyFileName)
	if err := platform.WriteFileAtomic(keyPath, []byte(id.String()+"\n"), 0o600); err != nil {
		return err
	}
	cfg := &config.Config{Storage: *st, KeyFile: homeRelative(keyPath, dirs.Home), ConditionalWrites: &conditional}
	if err := cfg.Save(dirs.State); err != nil {
		return err
	}
	if joined {
		a.success("Joined the existing bucket %s.", bucketName(st.Bucket, st.PathPrefix))
	} else {
		a.success("Set up the bucket %s.", bucketName(st.Bucket, st.PathPrefix))
	}

	if err := a.firstSync(ctx, newEngine(dirs, store, crypto.NewEncryptorFromIdentity(id)), o.yes); err != nil {
		return err
	}
	if _, _, missing := missingHere(ctx, dirs); len(missing) > 0 {
		a.printf("\n%sYour other machines have more installed:%s\n", colorBold, colorReset)
		for _, m := range missing {
			if m.Here != "" {
				a.printf("    %-16s %s here, %s on %s\n", m.Name, m.Here, m.Version, m.On)
			} else {
				a.printf("    %-16s %s\n", m.Name, installHint(m))
			}
		}
	}
	return a.offerAuto(ctx, dirs, o.yes)
}

// offerAuto offers automatic sync, unless it came with the settings of the
// other machines already.
func (a *app) offerAuto(ctx context.Context, dirs platform.Dirs, yes bool) error {
	on := false
	for _, st := range a.autoStates(ctx, dirs) {
		if len(st.Hooks) > 0 {
			on = true
		}
	}
	if on {
		a.printf("\n")
		a.success("Automatic sync came with your settings: it syncs when a session starts and after every answer.")
		for _, f := range missingPrograms(dirs) {
			a.warn("%s; install codeagent-sync there, or every prompt shows a hook error.", f.Detail)
		}
		if _, err := os.Stat(dirs.Codex); err == nil {
			a.codexTrustAdvice(ctx, dirs.Home)
		}
		return nil
	}
	if !a.interactive() {
		a.info("To sync automatically from Claude Code and Codex: codeagent-sync auto enable")
		return nil
	}
	a.printf("\n")
	if ok, err := a.confirm("Sync automatically, through hooks in Claude Code and Codex?", true, yes); err != nil || !ok {
		a.info("Turn it on later with: codeagent-sync auto enable")
		return nil // the setup itself is done
	}
	return a.autoEnable(ctx)
}

// chooseStorage takes the storage settings from flags, or else from the
// wizard.
func (a *app) chooseStorage(o initOptions, dirs platform.Dirs) (*storage.StorageConfig, error) {
	if o.storage.Provider != "" {
		st := o.storage
		setBucket(&st, o.bucket)
		if st.Provider == storage.ProviderGCS && st.CredentialsFile == "" {
			st.UseDefaultCredentials = true
		}
		return &st, st.Validate()
	}

	if !a.interactive() {
		return nil, errors.New("no storage settings: pass --provider and its flags, or run init in a terminal")
	}
	return a.storageWizard(o.bucket)
}

func setBucket(st *storage.StorageConfig, bucket string) {
	if st.Provider == storage.ProviderWebDAV {
		st.PathPrefix, st.Bucket = bucket, ""
	} else {
		st.Bucket = bucket
	}
}

// ensureBucket creates the bucket when it is missing and the credentials
// allow it.
func (a *app) ensureBucket(ctx context.Context, store storage.ObjectStore, st *storage.StorageConfig, yes bool) error {
	exists, err := store.BucketExists(ctx)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	creator, ok := store.(storage.BucketCreator)
	if !ok {
		return fmt.Errorf("the bucket %q does not exist; create it first", st.Bucket)
	}
	create, err := a.confirm(fmt.Sprintf("The bucket %q does not exist. Create it?", st.Bucket), true, yes)
	if err != nil {
		return err
	}
	if !create {
		return fmt.Errorf("the bucket %q does not exist", st.Bucket)
	}
	if err := creator.CreateBucket(ctx); err != nil {
		hint := ""
		if st.Provider == storage.ProviderR2 {
			hint = "\nCreate it at https://dash.cloudflare.com/?to=/:account/r2/new and make sure the API token can read and write it."
		}
		return fmt.Errorf("%w%s", err, hint)
	}
	a.success("Created the bucket %s.", st.Bucket)
	return nil
}

// firstSync shows what the first sync will do and runs it. Entries that
// exist only on this machine are uploaded only when confirmed.
func (a *app) firstSync(ctx context.Context, eng *engine.Engine, yes bool) error {
	plan, err := eng.Run(ctx, engine.Options{DryRun: true})
	if err != nil {
		return err
	}
	downloads := 0
	for _, x := range plan.Actions {
		if x.Kind == engine.Download || x.Kind == engine.RemoveLocal {
			downloads++
		}
	}

	adopt := false
	if n := len(plan.Pending); n > 0 {
		a.printf("\n%d entries exist only on this machine:\n", n)
		var paths []string
		for _, x := range plan.Pending {
			paths = append(paths, displayPath(x.Key))
		}
		a.printList(paths)
		adopt, err = a.confirm(fmt.Sprintf("Upload these %d entries so your other machines get them?", n), true, yes)
		if errors.Is(err, errNotInteractive) {
			a.info("Not uploaded; run: codeagent-sync sync --yes")
		} else if err != nil {
			return err
		}
	}
	if downloads > 0 {
		a.printf("\n%d entries come from your other machines; files this replaces are backed up.\n", downloads)
		ok, err := a.confirm("Apply them now?", true, yes)
		if err != nil && !errors.Is(err, errNotInteractive) {
			return err
		}
		if !ok {
			a.info("Nothing applied yet; run codeagent-sync sync when ready.")
			return nil
		}
	}

	res, err := eng.Run(ctx, engine.Options{AdoptLocal: adopt})
	if err != nil {
		return err
	}
	a.printf("\n")
	if err := a.report(res, false); err != nil {
		return err
	}
	a.printf("\nFrom now on run %scodeagent-sync sync%s on each machine to stay in step.\n", colorBold, colorReset)
	return nil
}

// homeRelative writes p with ~ when it lies in home, so the config file
// reads the same on every machine.
func homeRelative(p, home string) string {
	rel, err := filepath.Rel(home, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return p
	}
	if rel == "." {
		return "~"
	}
	return "~" + string(os.PathSeparator) + rel
}
