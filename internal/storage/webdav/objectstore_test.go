package webdav

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/sametbrr/codeagent-sync/internal/storage"
	"github.com/sametbrr/codeagent-sync/internal/storage/storagetest"
)

func TestSuiteAgainstFakeDAV(t *testing.T) {
	tests := []struct {
		name   string
		base   string // where the fake server mounts the user's files
		prefix string // the client's path_prefix
		setup  func(*fakeDAV)
	}{
		{name: "Nextcloud layout", base: "/remote.php/dav/files/ad/", prefix: "codeagent-sync"},
		// #89: the prefix also appears earlier in the URL path.
		{name: "user named like the prefix", base: "/remote.php/dav/files/codeagent-sync/", prefix: "codeagent-sync"},
		{name: "no path prefix", base: "/dav/", prefix: ""},
		// #88: servers that refuse Depth: infinity are walked with Depth: 1.
		{name: "no Depth infinity", base: "/webdav/", prefix: "sync", setup: func(f *fakeDAV) { f.noInfinity = true }},
		{name: "absolute hrefs", base: "/dav/", prefix: "sync", setup: func(f *fakeDAV) { f.absoluteHrefs = true }},
		{name: "no ETag on PUT", base: "/dav/", prefix: "sync", setup: func(f *fakeDAV) { f.noPutETag = true }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeDAV(tc.base)
			if tc.setup != nil {
				tc.setup(fake)
			}
			srv := httptest.NewServer(fake)
			t.Cleanup(srv.Close)

			s, err := New(&storage.StorageConfig{
				Provider:       storage.ProviderWebDAV,
				WebDAVURL:      srv.URL + tc.base,
				WebDAVUsername: "ad",
				WebDAVPassword: "app-password",
				PathPrefix:     tc.prefix,
			})
			if err != nil {
				t.Fatal(err)
			}
			if ok, err := s.BucketExists(context.Background()); !ok || err != nil {
				t.Fatalf("BucketExists = %v, %v", ok, err)
			}
			storagetest.Run(t, s, "suite/")
		})
	}
}

func TestHrefToKey(t *testing.T) {
	c := &Client{baseURL: "https://cloud.example.com/remote.php/dav/files/codeagent-sync", pathPrefix: "codeagent-sync"}
	tests := []struct {
		href, key string
		ok        bool
	}{
		{"/remote.php/dav/files/codeagent-sync/codeagent-sync/v1/a.age", "v1/a.age", true},
		{"https://cloud.example.com/remote.php/dav/files/codeagent-sync/codeagent-sync/v1/sp%20ace/%C5%9F.md", "v1/sp ace/ş.md", true},
		{"/remote.php/dav/files/codeagent-sync/codeagent-sync/dir/", "dir", true},
		{"/remote.php/dav/files/codeagent-sync/codeagent-sync/", "", true},
		{"/remote.php/dav/files/codeagent-sync/other/x", "", false},
	}
	for _, tc := range tests {
		key, ok := c.hrefToKey(tc.href)
		if key != tc.key || ok != tc.ok {
			t.Errorf("hrefToKey(%q) = %q, %v; want %q, %v", tc.href, key, ok, tc.key, tc.ok)
		}
	}
}

func TestNormalizeETag(t *testing.T) {
	for in, want := range map[string]string{
		`"abc"`:   `"abc"`,
		`abc`:     `"abc"`,
		` "abc" `: `"abc"`,
		`W/"abc"`: `W/"abc"`,
		``:        ``,
	} {
		if got := normalizeETag(in); got != want {
			t.Errorf("normalizeETag(%q) = %q, want %q", in, got, want)
		}
	}
}
