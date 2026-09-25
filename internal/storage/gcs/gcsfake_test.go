package gcs

import (
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// fakeGCS serves the part of the GCS API the client library uses when
// STORAGE_EMULATOR_HOST points at it: JSON API uploads (multipart), object
// metadata, listing and deletion, and XML API reads. Writes honor the
// ifGenerationMatch precondition (0 = the object must not exist).
type fakeGCS struct {
	bucket string

	mu      sync.Mutex
	objects map[string]gcsObject
	nextGen int64
}

type gcsObject struct {
	data       []byte
	generation int64
	updated    time.Time
}

func newFakeGCS(bucket string) *fakeGCS {
	return &fakeGCS{bucket: bucket, objects: map[string]gcsObject{}, nextGen: 1000}
}

func (f *fakeGCS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	p := r.URL.Path
	uploadPath := "/upload/storage/v1/b/" + f.bucket + "/o"
	objectsPath := "/storage/v1/b/" + f.bucket + "/o"
	switch {
	case p == uploadPath && r.Method == http.MethodPost:
		f.upload(w, r)
	case p == objectsPath && r.Method == http.MethodGet:
		f.list(w, r.URL.Query().Get("prefix"))
	case strings.HasPrefix(p, objectsPath+"/"):
		name := strings.TrimPrefix(p, objectsPath+"/")
		o, ok := f.objects[name]
		switch {
		case !ok:
			writeGCSError(w, http.StatusNotFound, "No such object")
		case r.Method == http.MethodGet:
			writeJSON(w, f.resource(name, o))
		case r.Method == http.MethodDelete:
			delete(f.objects, name)
			w.WriteHeader(http.StatusNoContent)
		default:
			writeGCSError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case p == "/storage/v1/b/"+f.bucket && r.Method == http.MethodGet:
		writeJSON(w, map[string]string{"kind": "storage#bucket", "name": f.bucket})
	case strings.HasPrefix(p, "/"+f.bucket+"/") && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		name := strings.TrimPrefix(p, "/"+f.bucket+"/")
		o, ok := f.objects[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("X-Goog-Generation", strconv.FormatInt(o.generation, 10))
		w.Header().Set("X-Goog-Metageneration", "1")
		w.Header().Set("Last-Modified", o.updated.UTC().Format(http.TimeFormat))
		w.Header().Set("Content-Length", strconv.Itoa(len(o.data)))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			w.Write(o.data)
		}
	default:
		writeGCSError(w, http.StatusNotImplemented, "not implemented: "+r.Method+" "+p)
	}
}

func (f *fakeGCS) upload(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("uploadType") != "multipart" {
		writeGCSError(w, http.StatusNotImplemented, "only multipart uploads are supported")
		return
	}
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		writeGCSError(w, http.StatusBadRequest, "expected a multipart body")
		return
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	metaPart, err := mr.NextPart()
	if err != nil {
		writeGCSError(w, http.StatusBadRequest, "missing metadata part")
		return
	}
	var meta struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(metaPart).Decode(&meta); err != nil {
		writeGCSError(w, http.StatusBadRequest, "bad metadata")
		return
	}
	mediaPart, err := mr.NextPart()
	if err != nil {
		writeGCSError(w, http.StatusBadRequest, "missing media part")
		return
	}
	data, _ := io.ReadAll(mediaPart)

	name := meta.Name
	if name == "" {
		name = q.Get("name")
	}
	cur, exists := f.objects[name]
	if v := q.Get("ifGenerationMatch"); v != "" {
		want, _ := strconv.ParseInt(v, 10, 64)
		if (want == 0 && exists) || (want != 0 && (!exists || cur.generation != want)) {
			writeGCSError(w, http.StatusPreconditionFailed, "Precondition Failed")
			return
		}
	}
	f.nextGen++
	o := gcsObject{data: data, generation: f.nextGen, updated: time.Now()}
	f.objects[name] = o
	writeJSON(w, f.resource(name, o))
}

func (f *fakeGCS) list(w http.ResponseWriter, prefix string) {
	var names []string
	for name := range f.objects {
		if strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	items := []map[string]any{}
	for _, name := range names {
		items = append(items, f.resource(name, f.objects[name]))
	}
	writeJSON(w, map[string]any{"kind": "storage#objects", "items": items})
}

func (f *fakeGCS) resource(name string, o gcsObject) map[string]any {
	return map[string]any{
		"kind":           "storage#object",
		"bucket":         f.bucket,
		"name":           name,
		"generation":     strconv.FormatInt(o.generation, 10),
		"metageneration": "1",
		"size":           strconv.Itoa(len(o.data)),
		"updated":        o.updated.UTC().Format(time.RFC3339Nano),
		"contentType":    "application/octet-stream",
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(v)
}

func writeGCSError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": msg}})
}
