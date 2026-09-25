package webdav

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/sametbrr/codeagent-sync/internal/storage"
)

func init() {
	storage.NewWebDAV = New
}

// Client implements storage for WebDAV servers (Nextcloud, ownCloud, etc.).
type Client struct {
	baseURL    string
	pathPrefix string
	httpClient *http.Client
	username   string
	password   string

	// madeDirs remembers collections that exist, so a PUT does not repeat
	// one MKCOL per parent directory every time.
	madeDirs sync.Map
}

var _ storage.ObjectStore = (*Client)(nil)

// New creates a new WebDAV storage client
func New(cfg *storage.StorageConfig) (storage.ObjectStore, error) {
	baseURL := strings.TrimRight(cfg.WebDAVURL, "/")
	if baseURL == "" {
		return nil, fmt.Errorf("WebDAV URL is required")
	}

	// Enforce HTTPS to protect Basic Auth credentials, except for localhost
	if !strings.HasPrefix(baseURL, "https://") {
		if !isLocalhost(baseURL) {
			return nil, fmt.Errorf("WebDAV URL must use HTTPS to protect credentials (use https:// instead of http://)")
		}
	}

	prefix := strings.Trim(cfg.PathPrefix, "/")

	return &Client{
		baseURL:    baseURL,
		pathPrefix: prefix,
		username:   cfg.WebDAVUsername,
		password:   cfg.WebDAVPassword,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}, nil
}

// isLocalhost checks if a URL points to localhost (safe for HTTP)
func isLocalhost(url string) bool {
	// Strip scheme
	host := strings.TrimPrefix(url, "http://")
	host = strings.TrimPrefix(host, "https://")
	// Get host part before any path
	if idx := strings.Index(host, "/"); idx > 0 {
		host = host[:idx]
	}
	// Remove port if present
	if idx := strings.LastIndex(host, ":"); idx > 0 {
		host = host[:idx]
	}
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// escapeKey percent-encodes each segment of a slash-separated key.
func escapeKey(key string) string {
	segments := strings.Split(key, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return strings.Join(segments, "/")
}

func (c *Client) fullURL(key string) string {
	return c.collectionURL() + escapeKey(key)
}

// collectionURL is the URL of the collection that holds every object.
func (c *Client) collectionURL() string {
	if c.pathPrefix != "" {
		return c.baseURL + "/" + escapeKey(c.pathPrefix) + "/"
	}
	return c.baseURL + "/"
}

// basePath is the decoded URL path of the collection. The href of every
// object in a PROPFIND answer starts with it.
func (c *Client) basePath() string {
	u, err := url.Parse(c.collectionURL())
	if err != nil {
		return "/"
	}
	return u.Path
}

// hrefToKey converts an href from a PROPFIND answer into a key relative to
// the collection. hrefs are percent-encoded and may be absolute URLs or
// absolute paths. ok is false for an href outside the collection; the
// collection's own href yields an empty key.
func (c *Client) hrefToKey(rawHref string) (key string, ok bool) {
	u, err := url.Parse(rawHref)
	if err != nil {
		return "", false
	}
	base := c.basePath()
	if !strings.HasPrefix(u.Path, base) {
		return "", false
	}
	return strings.TrimSuffix(u.Path[len(base):], "/"), true
}

// normalizeETag returns an ETag in its quoted wire form, the form If-Match
// expects. Some servers report getetag without quotes; weak ETags (W/"...")
// are kept as they are.
func normalizeETag(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || strings.HasPrefix(v, "W/") || strings.HasPrefix(v, `"`) {
		return v
	}
	return `"` + v + `"`
}

func (c *Client) doRequest(ctx context.Context, method, url string, body io.Reader, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.username, c.password)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return c.httpClient.Do(req)
}

// Put stores data at key if pre holds, creating parent collections as
// needed, and returns the ETag the server reports ("" if it reports none).
func (c *Client) Put(ctx context.Context, key string, data []byte, pre storage.Precondition) (string, error) {
	if err := c.ensureParentDirs(ctx, key); err != nil {
		return "", fmt.Errorf("put %s: create parent directories: %w", key, err)
	}

	headers := map[string]string{"Content-Type": "application/octet-stream"}
	if pre.IfNoneMatch {
		headers["If-None-Match"] = "*"
	}
	if pre.IfMatch != "" {
		headers["If-Match"] = pre.IfMatch
	}
	resp, err := c.doRequest(ctx, http.MethodPut, c.fullURL(key), bytes.NewReader(data), headers)
	if err != nil {
		return "", fmt.Errorf("put %s: %w", key, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusNoContent:
		return normalizeETag(resp.Header.Get("ETag")), nil
	case http.StatusPreconditionFailed:
		return "", fmt.Errorf("put %s: %w", key, storage.ErrPreconditionFailed)
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("put %s: HTTP %d: %s", key, resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

// Get returns an object's data and ETag.
func (c *Client) Get(ctx context.Context, key string) ([]byte, string, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, c.fullURL(key), nil, nil)
	if err != nil {
		return nil, "", fmt.Errorf("get %s: %w", key, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, "", fmt.Errorf("get %s: %w", key, storage.ErrNotFound)
	default:
		return nil, "", fmt.Errorf("get %s: HTTP %d", key, resp.StatusCode)
	}
	data, err := storage.ReadAllLimited(resp.Body, key)
	if err != nil {
		return nil, "", err
	}
	return data, normalizeETag(resp.Header.Get("ETag")), nil
}

// Delete removes the object with the given key.
func (c *Client) Delete(ctx context.Context, key string) error {
	resp, err := c.doRequest(ctx, "DELETE", c.fullURL(key), nil, nil)
	if err != nil {
		return fmt.Errorf("failed to delete %s: %w", key, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("failed to delete %s: HTTP %d", key, resp.StatusCode)
	}

	return nil
}

// propfindListBody is the PROPFIND request body used to enumerate objects.
// maxPropfindResponseSize is the maximum allowed size for PROPFIND XML responses (10MB).
// This prevents memory exhaustion from malicious servers sending huge XML payloads.
const maxPropfindResponseSize = 10 * 1024 * 1024

const propfindListBody = `<?xml version="1.0" encoding="UTF-8"?>
<d:propfind xmlns:d="DAV:">
  <d:prop>
    <d:getcontentlength/>
    <d:getlastmodified/>
    <d:getetag/>
    <d:resourcetype/>
  </d:prop>
</d:propfind>`

// List returns all objects whose keys start with prefix, using PROPFIND on
// the deepest collection that contains them.
//
// It first attempts a single Depth: infinity request, which Nextcloud and
// ownCloud support. Many other servers — notably Synology's WebDAV Server and
// Apache mod_dav with its default DavDepthInfinity off — reject infinite-depth
// PROPFIND with 403/405/501. In that case we fall back to walking the tree with
// Depth: 1 requests, which every WebDAV server supports.
func (c *Client) List(ctx context.Context, prefix string) ([]storage.ObjectInfo, error) {
	dir := prefix[:strings.LastIndex(prefix, "/")+1]
	startURL := c.collectionURL()
	if dir != "" {
		startURL += escapeKey(strings.TrimSuffix(dir, "/")) + "/"
	}

	responses, status, err := c.propfind(ctx, startURL, "infinity")
	if err != nil {
		return nil, fmt.Errorf("failed to list objects: %w", err)
	}

	var objects []storage.ObjectInfo
	switch {
	case status == http.StatusNotFound:
		return nil, nil
	case status == 207:
		objects = c.collectObjects(responses)
	case infinityUnsupported(status):
		// Server refuses Depth: infinity — walk the tree one level at a time.
		if objects, err = c.listRecursive(ctx, startURL); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("failed to list objects: HTTP %d", status)
	}

	var matched []storage.ObjectInfo
	for _, o := range objects {
		if strings.HasPrefix(o.Key, prefix) {
			matched = append(matched, o)
		}
	}
	return matched, nil
}

// infinityUnsupported reports whether a status code indicates the server
// rejected a Depth: infinity PROPFIND (as opposed to a genuine error).
func infinityUnsupported(status int) bool {
	switch status {
	case http.StatusForbidden, http.StatusMethodNotAllowed, http.StatusNotImplemented, http.StatusBadRequest:
		return true
	default:
		return false
	}
}

// listRecursive walks the collection tree using Depth: 1 PROPFIND requests,
// for servers that reject Depth: infinity. Directories are visited breadth-first.
func (c *Client) listRecursive(ctx context.Context, startURL string) ([]storage.ObjectInfo, error) {
	var objects []storage.ObjectInfo
	queue := []string{startURL}
	visited := map[string]bool{}

	for len(queue) > 0 {
		dirURL := queue[0]
		queue = queue[1:]
		if visited[dirURL] {
			continue
		}
		visited[dirURL] = true

		responses, status, err := c.propfind(ctx, dirURL, "1")
		if err != nil {
			return nil, fmt.Errorf("failed to list objects: %w", err)
		}
		if status == http.StatusNotFound {
			continue
		}
		if status != 207 {
			return nil, fmt.Errorf("failed to list objects: HTTP %d", status)
		}

		for _, r := range responses {
			key, ok := c.hrefToKey(r.RawHref)
			if !ok || key == "" {
				continue // outside the collection, or the collection itself
			}
			if r.IsCollection {
				queue = append(queue, c.collectionURL()+escapeKey(key)+"/")
				continue
			}
			objects = append(objects, r.objectInfo(key))
		}
	}

	return objects, nil
}

// propfind issues a PROPFIND at the given depth and returns the parsed entries
// together with the HTTP status code. A non-207 status yields (nil, status, nil)
// so callers can decide how to react without treating it as a transport error.
func (c *Client) propfind(ctx context.Context, url, depth string) ([]parsedResponse, int, error) {
	resp, err := c.doRequest(ctx, "PROPFIND", url, strings.NewReader(propfindListBody), map[string]string{
		"Content-Type": "application/xml",
		"Depth":        depth,
	})
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 207 {
		return nil, resp.StatusCode, nil
	}

	// Limit response size to prevent memory exhaustion from large XML payloads
	limited := io.LimitReader(resp.Body, maxPropfindResponseSize+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to read PROPFIND response: %w", err)
	}
	if int64(len(body)) > maxPropfindResponseSize {
		return nil, resp.StatusCode, fmt.Errorf("PROPFIND response exceeds maximum size of %d bytes", maxPropfindResponseSize)
	}

	responses, err := parsePropfindResponse(body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to parse PROPFIND response: %w", err)
	}

	return responses, resp.StatusCode, nil
}

// collectObjects turns PROPFIND entries into ObjectInfos, dropping collections
// and entries outside the collection.
func (c *Client) collectObjects(responses []parsedResponse) []storage.ObjectInfo {
	var objects []storage.ObjectInfo
	for _, r := range responses {
		if r.IsCollection {
			continue
		}
		key, ok := c.hrefToKey(r.RawHref)
		if !ok || key == "" {
			continue
		}
		objects = append(objects, r.objectInfo(key))
	}
	return objects
}

func (r parsedResponse) objectInfo(key string) storage.ObjectInfo {
	return storage.ObjectInfo{
		Key:          key,
		Size:         r.ContentLength,
		LastModified: r.LastModified,
		ETag:         r.ETag,
	}
}

// Head returns metadata for the given key without downloading content.
func (c *Client) Head(ctx context.Context, key string) (*storage.ObjectInfo, error) {
	responses, status, err := c.propfind(ctx, c.fullURL(key), "0")
	if err != nil {
		return nil, fmt.Errorf("head %s: %w", key, err)
	}
	if status == http.StatusNotFound {
		return nil, fmt.Errorf("head %s: %w", key, storage.ErrNotFound)
	}
	if status != 207 {
		return nil, fmt.Errorf("head %s: HTTP %d", key, status)
	}
	if len(responses) == 0 || responses[0].IsCollection {
		return nil, fmt.Errorf("head %s: %w", key, storage.ErrNotFound)
	}
	info := responses[0].objectInfo(key)
	return &info, nil
}

// BucketExists checks if the configured path prefix exists and is accessible.
// For WebDAV, the "bucket" concept maps to the path prefix directory.
// Unlike S3/R2/GCS where buckets must be pre-created, WebDAV directories
// can be created on the fly, so this auto-creates the path prefix if missing.
func (c *Client) BucketExists(ctx context.Context) (bool, error) {
	resp, err := c.doRequest(ctx, "PROPFIND", c.collectionURL(), strings.NewReader(`<?xml version="1.0" encoding="UTF-8"?>
<d:propfind xmlns:d="DAV:">
  <d:prop>
    <d:resourcetype/>
  </d:prop>
</d:propfind>`), map[string]string{
		"Content-Type": "application/xml",
		"Depth":        "0",
	})
	if err != nil {
		return false, fmt.Errorf("failed to check WebDAV path: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == 207 || resp.StatusCode == http.StatusOK {
		return true, nil
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return false, fmt.Errorf("authentication failed (HTTP %d) - check your username and app password", resp.StatusCode)
	}

	if resp.StatusCode == http.StatusNotFound && c.pathPrefix != "" {
		// Auto-create the path prefix directory
		mkResp, mkErr := c.doRequest(ctx, "MKCOL", c.collectionURL(), nil, nil)
		if mkErr != nil {
			return false, fmt.Errorf("failed to create WebDAV directory '%s': %w", c.pathPrefix, mkErr)
		}
		_ = mkResp.Body.Close()
		if mkResp.StatusCode == http.StatusCreated || mkResp.StatusCode == http.StatusMethodNotAllowed {
			return true, nil
		}
		return false, fmt.Errorf("failed to create WebDAV directory '%s': HTTP %d", c.pathPrefix, mkResp.StatusCode)
	}

	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}

	return false, fmt.Errorf("unexpected HTTP %d checking WebDAV path", resp.StatusCode)
}

// ensureParentDirs creates all parent collections for the given key via MKCOL.
// A failure here is not reported: the PUT that follows fails with the
// server's actual reason.
func (c *Client) ensureParentDirs(ctx context.Context, key string) error {
	dir := path.Dir(key)
	if dir == "." || dir == "/" || dir == "" {
		return nil
	}

	current := ""
	for _, part := range strings.Split(dir, "/") {
		if part == "" {
			continue
		}
		if current == "" {
			current = part
		} else {
			current = current + "/" + part
		}
		if _, ok := c.madeDirs.Load(current); ok {
			continue
		}

		resp, err := c.doRequest(ctx, "MKCOL", c.collectionURL()+escapeKey(current)+"/", nil, nil)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		// 201 = created, 405 = already exists
		if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusMethodNotAllowed {
			c.madeDirs.Store(current, true)
		}
	}

	return nil
}
