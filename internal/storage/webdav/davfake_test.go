package webdav

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// fakeDAV is an in-memory WebDAV server mounted under base (for example
// "/remote.php/dav/files/ad/"), with RFC 7232 preconditions on PUT. Options
// reproduce behaviors of real servers.
type fakeDAV struct {
	base string

	noInfinity    bool // reject Depth: infinity, like Synology and Apache
	absoluteHrefs bool // answer PROPFIND with absolute URLs
	noPutETag     bool // send no ETag header on PUT

	mu    sync.Mutex
	files map[string]davFile // decoded path relative to base
	dirs  map[string]bool    // decoded relative paths; "" is base itself
	seq   int
}

type davFile struct {
	data     []byte
	etag     string
	modified time.Time
}

func newFakeDAV(base string) *fakeDAV {
	return &fakeDAV{base: base, files: map[string]davFile{}, dirs: map[string]bool{"": true}}
}

func (f *fakeDAV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if p+"/" == f.base {
		p = f.base
	}
	if !strings.HasPrefix(p, f.base) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	rel := strings.TrimSuffix(strings.TrimPrefix(p, f.base), "/")

	f.mu.Lock()
	defer f.mu.Unlock()

	switch r.Method {
	case "MKCOL":
		if f.dirs[rel] || f.hasFile(rel) {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !f.dirs[parentOf(rel)] {
			w.WriteHeader(http.StatusConflict)
			return
		}
		f.dirs[rel] = true
		w.WriteHeader(http.StatusCreated)

	case http.MethodPut:
		if !f.dirs[parentOf(rel)] {
			w.WriteHeader(http.StatusConflict)
			return
		}
		data, _ := io.ReadAll(r.Body)
		cur, exists := f.files[rel]
		if r.Header.Get("If-None-Match") == "*" && exists {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		if m := r.Header.Get("If-Match"); m != "" && (!exists || m != cur.etag) {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		f.seq++
		file := davFile{data: data, etag: `"` + strconv.Itoa(f.seq) + `"`, modified: time.Now()}
		f.files[rel] = file
		if !f.noPutETag {
			w.Header().Set("ETag", file.etag)
		}
		if exists {
			w.WriteHeader(http.StatusNoContent)
		} else {
			w.WriteHeader(http.StatusCreated)
		}

	case http.MethodGet:
		file, ok := f.files[rel]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("ETag", file.etag)
		w.WriteHeader(http.StatusOK)
		w.Write(file.data)

	case http.MethodDelete:
		if _, ok := f.files[rel]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		delete(f.files, rel)
		w.WriteHeader(http.StatusNoContent)

	case "PROPFIND":
		depth := r.Header.Get("Depth")
		if depth == "infinity" && f.noInfinity {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		var entries []string
		switch {
		case f.hasFile(rel):
			entries = []string{rel}
		case f.dirs[rel]:
			entries = []string{rel + "/"}
			if depth != "0" {
				entries = append(entries, f.children(rel, depth == "infinity")...)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(207)
		io.WriteString(w, f.multistatus(r, entries))

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (f *fakeDAV) hasFile(rel string) bool {
	_, ok := f.files[rel]
	return ok
}

// children lists the files and collections (with a trailing slash) under dir.
func (f *fakeDAV) children(dir string, recursive bool) []string {
	within := func(p string) bool {
		if dir == "" {
			return p != "" && (recursive || !strings.Contains(p, "/"))
		}
		if !strings.HasPrefix(p, dir+"/") {
			return false
		}
		return recursive || !strings.Contains(p[len(dir)+1:], "/")
	}
	var out []string
	for p := range f.files {
		if within(p) {
			out = append(out, p)
		}
	}
	for p := range f.dirs {
		if within(p) {
			out = append(out, p+"/")
		}
	}
	sort.Strings(out)
	return out
}

func (f *fakeDAV) multistatus(r *http.Request, entries []string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?><d:multistatus xmlns:d="DAV:">`)
	for _, e := range entries {
		href := f.base + escapePath(strings.TrimSuffix(e, "/"))
		if strings.HasSuffix(e, "/") && e != "/" {
			href += "/"
		} else if e == "/" {
			href = f.base
		}
		if f.absoluteHrefs {
			href = "http://" + r.Host + href
		}
		b.WriteString(`<d:response><d:href>`)
		xml.EscapeText(&b, []byte(href))
		b.WriteString(`</d:href><d:propstat><d:prop>`)
		if strings.HasSuffix(e, "/") {
			b.WriteString(`<d:resourcetype><d:collection/></d:resourcetype>`)
		} else {
			file := f.files[e]
			fmt.Fprintf(&b, `<d:resourcetype/><d:getcontentlength>%d</d:getcontentlength>`, len(file.data))
			fmt.Fprintf(&b, `<d:getlastmodified>%s</d:getlastmodified>`, file.modified.UTC().Format(http.TimeFormat))
			b.WriteString(`<d:getetag>`)
			xml.EscapeText(&b, []byte(file.etag))
			b.WriteString(`</d:getetag>`)
		}
		b.WriteString(`</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`)
	}
	b.WriteString(`</d:multistatus>`)
	return b.String()
}

func escapePath(p string) string {
	if p == "" {
		return ""
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

func parentOf(rel string) string {
	d := path.Dir(rel)
	if d == "." {
		return ""
	}
	return d
}
