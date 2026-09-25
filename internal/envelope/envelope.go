// Package envelope is the format of synced objects: an entry's metadata and
// content, compressed and then encrypted.
//
// The plaintext of an object is
//
//	"codeagent-sync/1\n" | header length (4 bytes, big endian) | header JSON | content
//
// and the object is age(gzip(plaintext)). The header carries the object's own
// remote key, so a party with write access to the bucket but without the key
// cannot move valid ciphertexts to other keys (for example, put one skill's
// content where settings.json belongs).
package envelope

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// Kind is what an object holds.
type Kind string

const (
	KindFile      Kind = "file"
	KindLink      Kind = "link"      // a link to another synced entry
	KindTombstone Kind = "tombstone" // the entry was deleted
)

// Header describes the entry stored in an object.
type Header struct {
	Kind Kind `json:"kind"`
	// Key is the object's remote key; Open rejects an object whose header
	// names a different key.
	Key string `json:"key"`

	// Files.
	Executable bool      `json:"exec,omitempty"`
	ModTime    time.Time `json:"mtime,omitzero"`
	SHA256     string    `json:"sha256,omitempty"` // hex digest of the content
	Size       int64     `json:"size,omitempty"`

	// Links: the target as "<root>/<rel>" (for example "agents/skills/foo"),
	// so each machine can build the right relative link for its own layout.
	LinkTarget string `json:"target,omitempty"`
	LinkIsDir  bool   `json:"target_is_dir,omitempty"`

	// Who wrote the object and when, for diagnostics and conflict messages.
	Machine string    `json:"machine,omitempty"`
	Written time.Time `json:"written"`
}

// Cipher encrypts and decrypts object bodies; *crypto.Encryptor implements it.
type Cipher interface {
	Encrypt(plaintext []byte) ([]byte, error)
	Decrypt(ciphertext []byte) ([]byte, error)
}

const magic = "codeagent-sync/1\n"

// maxHeaderSize and MaxContentSize bound what Open accepts, so a corrupt or
// hostile object cannot exhaust memory when decompressed.
const (
	maxHeaderSize  = 64 << 10
	MaxContentSize = 100 << 20
)

// ErrKeyMismatch means an object's header names a different key than the one
// it was read from.
var ErrKeyMismatch = errors.New("object belongs to a different key")

// Hash returns the hex SHA-256 of content, as recorded in Header.SHA256.
func Hash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// Seal builds the object for h and content. For files it fills in SHA256
// and Size; the caller sets everything else.
func Seal(c Cipher, h Header, content []byte) ([]byte, error) {
	if h.Kind == KindFile {
		h.SHA256 = Hash(content)
		h.Size = int64(len(content))
	} else if len(content) > 0 {
		return nil, fmt.Errorf("envelope: a %s entry has no content", h.Kind)
	}
	if len(content) > MaxContentSize {
		return nil, fmt.Errorf("envelope: %s is %d bytes, over the %d byte limit", h.Key, len(content), MaxContentSize)
	}
	header, err := json.Marshal(h)
	if err != nil {
		return nil, err
	}

	var zipped bytes.Buffer
	zw := gzip.NewWriter(&zipped)
	// Writes to a gzip writer over a buffer fail only through Close.
	_, _ = zw.Write([]byte(magic))
	_ = binary.Write(zw, binary.BigEndian, uint32(len(header)))
	_, _ = zw.Write(header)
	_, _ = zw.Write(content)
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return c.Encrypt(zipped.Bytes())
}

// Open decrypts and checks an object read from key.
func Open(c Cipher, key string, object []byte) (Header, []byte, error) {
	zipped, err := c.Decrypt(object)
	if err != nil {
		return Header{}, nil, fmt.Errorf("envelope: %s: %w", key, err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(zipped))
	if err != nil {
		return Header{}, nil, fmt.Errorf("envelope: %s: %w", key, err)
	}
	plain, err := io.ReadAll(io.LimitReader(zr, int64(len(magic))+4+maxHeaderSize+MaxContentSize+1))
	if err != nil {
		return Header{}, nil, fmt.Errorf("envelope: %s: %w", key, err)
	}

	if !bytes.HasPrefix(plain, []byte(magic)) {
		return Header{}, nil, fmt.Errorf("envelope: %s: unknown format", key)
	}
	plain = plain[len(magic):]
	if len(plain) < 4 {
		return Header{}, nil, fmt.Errorf("envelope: %s: truncated", key)
	}
	n := binary.BigEndian.Uint32(plain)
	plain = plain[4:]
	if n > maxHeaderSize || int(n) > len(plain) {
		return Header{}, nil, fmt.Errorf("envelope: %s: bad header length %d", key, n)
	}
	var h Header
	if err := json.Unmarshal(plain[:n], &h); err != nil {
		return Header{}, nil, fmt.Errorf("envelope: %s: header: %w", key, err)
	}
	content := plain[n:]

	if h.Key != key {
		return Header{}, nil, fmt.Errorf("envelope: %s: %w (%s)", key, ErrKeyMismatch, h.Key)
	}
	switch h.Kind {
	case KindFile:
		if len(content) > MaxContentSize {
			return Header{}, nil, fmt.Errorf("envelope: %s: content over the %d byte limit", key, MaxContentSize)
		}
		if int64(len(content)) != h.Size || Hash(content) != h.SHA256 {
			return Header{}, nil, fmt.Errorf("envelope: %s: content does not match its header", key)
		}
	case KindLink, KindTombstone:
		if len(content) != 0 {
			return Header{}, nil, fmt.Errorf("envelope: %s: unexpected content in a %s", key, h.Kind)
		}
	default:
		return Header{}, nil, fmt.Errorf("envelope: %s: unknown kind %q", key, h.Kind)
	}
	return h, content, nil
}
