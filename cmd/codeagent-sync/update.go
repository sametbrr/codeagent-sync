package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sametbrr/codeagent-sync/internal/util"
)

type GitHubRelease struct {
	TagName string `json:"tag_name"`
	Name    string `json:"name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func (a *app) updateCmd() *cobra.Command {
	var checkOnly bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update codeagent-sync to the latest release",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			release, err := getLatestRelease()
			if err != nil {
				return fmt.Errorf("check for updates: %w", err)
			}
			latest := strings.TrimPrefix(release.TagName, "v")
			current := strings.TrimPrefix(version, "v")
			if util.CompareVersions(current, latest) >= 0 {
				a.success("Up to date (v%s).", current)
				return nil
			}
			a.printf("%s↑%s v%s is available (this is v%s)\n", colorCyan, colorReset, latest, current)
			if checkOnly {
				return nil
			}

			assetName := util.GetBinaryName(latest)
			var downloadURL string
			for _, asset := range release.Assets {
				if asset.Name == assetName {
					downloadURL = asset.BrowserDownloadURL
				}
			}
			if downloadURL == "" {
				return fmt.Errorf("the release has no binary for %s/%s", runtime.GOOS, runtime.GOARCH)
			}
			newBinary, err := downloadBinary(downloadURL)
			if err != nil {
				return fmt.Errorf("download the update: %w", err)
			}
			if err := verifyChecksum(release, assetName, newBinary); err != nil {
				return fmt.Errorf("refusing to install the update: %w", err)
			}
			execPath, err := os.Executable()
			if err != nil {
				return err
			}
			if execPath, err = filepath.EvalSymlinks(execPath); err != nil {
				return err
			}
			if err := replaceBinary(execPath, newBinary); err != nil {
				return fmt.Errorf("install the update: %w", err)
			}
			a.success("Updated to v%s.", latest)
			return nil
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false, "only check for a newer release")
	return cmd
}

func getLatestRelease() (*GitHubRelease, error) {
	req, err := http.NewRequest("GET", "https://api.github.com/repos/sametbrr/codeagent-sync/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "codeagent-sync/"+version)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}
	var release GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, err
	}
	return &release, nil
}

// verifyChecksum validates the downloaded binary against the checksums.txt
// asset published with the release. Releases predating checksum publication
// warn instead of failing; once checksums.txt is present, a missing entry or
// a mismatch aborts the update.
func verifyChecksum(release *GitHubRelease, assetName string, data []byte) error {
	var checksumsURL string
	for _, asset := range release.Assets {
		if asset.Name == "checksums.txt" {
			checksumsURL = asset.BrowserDownloadURL
			break
		}
	}
	if checksumsURL == "" {
		fmt.Printf("%s!%s Release has no checksums.txt; skipping integrity verification\n", colorYellow, colorReset)
		return nil
	}

	body, err := downloadBinary(checksumsURL)
	if err != nil {
		return fmt.Errorf("failed to download checksums.txt: %w", err)
	}

	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])

	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		// sha256sum format: "<hash>  <filename>" ("*" prefix = binary mode)
		if strings.TrimPrefix(fields[1], "*") != assetName {
			continue
		}
		if !strings.EqualFold(fields[0], got) {
			return fmt.Errorf("sha256 mismatch for %s: release lists %s, downloaded %s", assetName, fields[0], got)
		}
		fmt.Printf("%s✓%s Checksum verified\n", colorGreen, colorReset)
		return nil
	}
	return fmt.Errorf("checksums.txt has no entry for %s", assetName)
}

// maxBinarySize is the maximum allowed size for update binary downloads (200MB).
const maxBinarySize = 200 * 1024 * 1024

func downloadBinary(url string) ([]byte, error) {
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed with status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBinarySize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBinarySize {
		return nil, fmt.Errorf("binary exceeds maximum size of %d bytes", maxBinarySize)
	}
	return data, nil
}

func replaceBinary(execPath string, newBinary []byte) error {
	tmpPath := execPath + ".new"
	if err := os.WriteFile(tmpPath, newBinary, 0o755); err != nil {
		return fmt.Errorf("failed to write new binary: %w", err)
	}
	backupPath := execPath + ".old"
	if err := os.Rename(execPath, backupPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to backup current binary: %w", err)
	}
	if err := os.Rename(tmpPath, execPath); err != nil {
		_ = os.Rename(backupPath, execPath)
		return fmt.Errorf("failed to install new binary: %w", err)
	}
	_ = os.Remove(backupPath)
	return nil
}
