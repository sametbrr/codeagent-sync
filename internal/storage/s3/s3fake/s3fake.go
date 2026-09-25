// Package s3fake is a minimal S3 API server for tests: path-style PutObject,
// GetObject, HeadObject, DeleteObject, ListObjectsV2 and HeadBucket, with the
// conditional-write rules S3 and R2 apply (If-None-Match: * and If-Match).
package s3fake

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/xml"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Server is an http.Handler serving a single in-memory bucket.
type Server struct {
	Bucket string

	mu      sync.Mutex
	objects map[string]object
	forced  map[string]forcedError
}

type object struct {
	data     []byte
	etag     string
	modified time.Time
}

type forcedError struct {
	status int
	code   string
}

// New returns a server for the named bucket.
func New(bucket string) *Server {
	return &Server{Bucket: bucket, objects: make(map[string]object), forced: make(map[string]forcedError)}
}

// Force makes every request for key fail with status and S3 error code.
func (s *Server) Force(key string, status int, code string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forced[key] = forcedError{status, code}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	bucket, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if bucket != s.Bucket {
		writeError(w, http.StatusNotFound, "NoSuchBucket")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if f, ok := s.forced[key]; ok && key != "" {
		writeError(w, f.status, f.code)
		return
	}

	switch {
	case key == "" && r.Method == http.MethodHead:
		w.WriteHeader(http.StatusOK)
	case key == "" && r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2":
		s.list(w, r.URL.Query().Get("prefix"))
	case r.Method == http.MethodPut:
		s.put(w, r, key)
	case r.Method == http.MethodGet, r.Method == http.MethodHead:
		o, ok := s.objects[key]
		if !ok {
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeError(w, http.StatusNotFound, "NoSuchKey")
			return
		}
		w.Header().Set("ETag", o.etag)
		w.Header().Set("Content-Length", strconv.Itoa(len(o.data)))
		w.Header().Set("Last-Modified", o.modified.UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			w.Write(o.data)
		}
	case r.Method == http.MethodDelete:
		delete(s.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		writeError(w, http.StatusNotImplemented, "NotImplemented")
	}
}

func (s *Server) put(w http.ResponseWriter, r *http.Request, key string) {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "IncompleteBody")
		return
	}
	cur, exists := s.objects[key]
	if r.Header.Get("If-None-Match") == "*" && exists {
		writeError(w, http.StatusPreconditionFailed, "PreconditionFailed")
		return
	}
	if m := r.Header.Get("If-Match"); m != "" && (!exists || cur.etag != m) {
		writeError(w, http.StatusPreconditionFailed, "PreconditionFailed")
		return
	}
	sum := md5.Sum(data)
	o := object{data: data, etag: `"` + hex.EncodeToString(sum[:]) + `"`, modified: time.Now()}
	s.objects[key] = o
	w.Header().Set("ETag", o.etag)
	w.WriteHeader(http.StatusOK)
}

type listResult struct {
	XMLName     xml.Name   `xml:"ListBucketResult"`
	Name        string     `xml:"Name"`
	Prefix      string     `xml:"Prefix"`
	KeyCount    int        `xml:"KeyCount"`
	MaxKeys     int        `xml:"MaxKeys"`
	IsTruncated bool       `xml:"IsTruncated"`
	Contents    []listItem `xml:"Contents"`
}

type listItem struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int    `xml:"Size"`
}

func (s *Server) list(w http.ResponseWriter, prefix string) {
	res := listResult{Name: s.Bucket, Prefix: prefix, MaxKeys: 1000}
	for key, o := range s.objects {
		if strings.HasPrefix(key, prefix) {
			res.Contents = append(res.Contents, listItem{
				Key:          key,
				LastModified: o.modified.UTC().Format("2006-01-02T15:04:05.000Z"),
				ETag:         o.etag,
				Size:         len(o.data),
			})
		}
	}
	sort.Slice(res.Contents, func(i, j int) bool { return res.Contents[i].Key < res.Contents[j].Key })
	res.KeyCount = len(res.Contents)
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, xml.Header)
	xml.NewEncoder(w).Encode(res)
}

func writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	io.WriteString(w, xml.Header)
	xml.NewEncoder(w).Encode(struct {
		XMLName xml.Name `xml:"Error"`
		Code    string   `xml:"Code"`
		Message string   `xml:"Message"`
	}{Code: code, Message: code})
}
