package main

import (
	"fmt"

	"github.com/AlecAivazis/survey/v2"

	"github.com/sametbrr/codeagent-sync/internal/storage"
)

// storageWizard asks which storage to use and its settings.
func (a *app) storageWizard(bucket string) (*storage.StorageConfig, error) {
	choices := map[string]storage.Provider{
		"Cloudflare R2": storage.ProviderR2,
		"Amazon S3":     storage.ProviderS3,
		"S3-compatible (Backblaze B2, MinIO, …)":    "s3-compatible",
		"Google Cloud Storage":                      storage.ProviderGCS,
		"WebDAV (Nextcloud, ownCloud, Synology, …)": storage.ProviderWebDAV,
	}
	options := []string{"Cloudflare R2", "Amazon S3", "S3-compatible (Backblaze B2, MinIO, …)", "Google Cloud Storage", "WebDAV (Nextcloud, ownCloud, Synology, …)"}
	var choice string
	if err := survey.AskOne(&survey.Select{Message: "Where should the encrypted data live?", Options: options}, &choice); err != nil {
		return nil, err
	}

	cfg := &storage.StorageConfig{Provider: choices[choice], Bucket: bucket}
	var qs []*survey.Question

	input := func(name, msg, def string) *survey.Question {
		return &survey.Question{Name: name, Prompt: &survey.Input{Message: msg, Default: def}, Validate: survey.Required}
	}
	secret := func(name, msg string) *survey.Question {
		return &survey.Question{Name: name, Prompt: &survey.Password{Message: msg}, Validate: survey.Required}
	}

	answers := map[string]any{}
	switch choices[choice] {
	case storage.ProviderR2:
		a.info("Needs an R2 API token with read and write access to the bucket")
		a.info("(dash.cloudflare.com → R2 → Manage API tokens).")
		qs = []*survey.Question{input("account", "Account ID:", ""), input("key", "Access key ID:", ""), secret("secret", "Secret access key:"), input("bucket", "Bucket:", bucket)}
	case storage.ProviderS3:
		qs = []*survey.Question{input("key", "Access key ID:", ""), secret("secret", "Secret access key:"), input("region", "Region:", "us-east-1"), input("bucket", "Bucket:", bucket)}
	case "s3-compatible":
		qs = []*survey.Question{input("endpoint", "Endpoint URL:", ""), input("key", "Access key ID:", ""), secret("secret", "Secret access key:"), input("bucket", "Bucket:", bucket)}
	case storage.ProviderGCS:
		a.info("Uses the credentials file you give, or Application Default Credentials.")
		qs = []*survey.Question{input("project", "Project ID:", ""), {Name: "credentials", Prompt: &survey.Input{Message: "Credentials file (empty for default credentials):"}}, input("bucket", "Bucket:", bucket)}
	case storage.ProviderWebDAV:
		a.info("For Nextcloud use an app password; the URL looks like")
		a.info("https://cloud.example.com/remote.php/dav/files/USERNAME")
		qs = []*survey.Question{input("url", "WebDAV URL:", ""), input("user", "User name:", ""), secret("password", "Password or app password:"), input("prefix", "Folder:", bucket)}
	}
	if err := survey.Ask(qs, &answers); err != nil {
		return nil, err
	}
	get := func(k string) string {
		if v, ok := answers[k].(string); ok {
			return v
		}
		return ""
	}

	switch choices[choice] {
	case storage.ProviderR2:
		cfg.AccountID, cfg.AccessKeyID, cfg.SecretAccessKey, cfg.Bucket = get("account"), get("key"), get("secret"), get("bucket")
	case storage.ProviderS3:
		cfg.AccessKeyID, cfg.SecretAccessKey, cfg.Region, cfg.Bucket = get("key"), get("secret"), get("region"), get("bucket")
	case "s3-compatible":
		cfg.Provider = storage.ProviderS3
		cfg.Endpoint = storage.NormalizeEndpoint(get("endpoint"))
		cfg.Region = storage.RegionFromEndpoint(cfg.Endpoint)
		cfg.AccessKeyID, cfg.SecretAccessKey, cfg.Bucket = get("key"), get("secret"), get("bucket")
		pathStyle := false
		if err := survey.AskOne(&survey.Confirm{Message: "Use path-style addressing (needed by MinIO, Ceph)?", Default: false}, &pathStyle); err != nil {
			return nil, err
		}
		cfg.UsePathStyle = pathStyle
	case storage.ProviderGCS:
		cfg.ProjectID, cfg.CredentialsFile, cfg.Bucket = get("project"), get("credentials"), get("bucket")
		cfg.UseDefaultCredentials = cfg.CredentialsFile == ""
	case storage.ProviderWebDAV:
		cfg.WebDAVURL, cfg.WebDAVUsername, cfg.WebDAVPassword = get("url"), get("user"), get("password")
		cfg.PathPrefix, cfg.Bucket = get("prefix"), ""
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("storage settings: %w", err)
	}
	return cfg, nil
}
