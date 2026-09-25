package envelope

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/sametbrr/codeagent-sync/internal/crypto"
)

func newCipher(t *testing.T) Cipher {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	return crypto.NewEncryptorFromIdentity(id)
}

func TestFileRoundTrip(t *testing.T) {
	c := newCipher(t)
	mtime := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	content := []byte("#!/bin/sh\necho hello\n")

	obj, err := Seal(c, Header{
		Kind: KindFile, Key: "v1/agents/skills/foo/run.sh",
		Executable: true, ModTime: mtime, Machine: "mac", Written: mtime,
	}, content)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(obj, content) {
		t.Fatal("content is visible in the sealed object")
	}

	h, got, err := Open(c, "v1/agents/skills/foo/run.sh", obj)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("content = %q, want %q", got, content)
	}
	if !h.Executable || !h.ModTime.Equal(mtime) || h.Machine != "mac" || h.SHA256 != Hash(content) || h.Size != int64(len(content)) {
		t.Errorf("header = %+v", h)
	}
}

func TestLinkAndTombstoneRoundTrip(t *testing.T) {
	c := newCipher(t)
	for _, h := range []Header{
		{Kind: KindLink, Key: "v1/claude/skills/foo", LinkTarget: "../../.agents/skills/foo", LinkIsDir: true},
		{Kind: KindTombstone, Key: "v1/claude/CLAUDE.md", Machine: "linux-box"},
	} {
		obj, err := Seal(c, h, nil)
		if err != nil {
			t.Fatal(err)
		}
		got, content, err := Open(c, h.Key, obj)
		if err != nil {
			t.Fatal(err)
		}
		if len(content) != 0 || got.Kind != h.Kind || got.LinkTarget != h.LinkTarget || got.LinkIsDir != h.LinkIsDir {
			t.Errorf("round trip of %+v gave %+v, %q", h, got, content)
		}
	}
	if _, err := Seal(c, Header{Kind: KindTombstone, Key: "k"}, []byte("x")); err == nil {
		t.Error("Seal accepted content for a tombstone")
	}
}

// An object copied to another key must be rejected even though it decrypts.
func TestOpenRejectsObjectsMovedToAnotherKey(t *testing.T) {
	c := newCipher(t)
	obj, err := Seal(c, Header{Kind: KindFile, Key: "v1/agents/skills/foo/SKILL.md"}, []byte("skill"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Open(c, "v1/claude/settings.json", obj); !errors.Is(err, ErrKeyMismatch) {
		t.Errorf("Open under another key: %v, want ErrKeyMismatch", err)
	}
}

func TestOpenRejectsForeignAndCorruptObjects(t *testing.T) {
	c := newCipher(t)
	other := newCipher(t)

	obj, _ := Seal(c, Header{Kind: KindFile, Key: "k"}, []byte("data"))
	if _, _, err := Open(other, "k", obj); err == nil {
		t.Error("Open with the wrong key succeeded")
	}

	sealRaw := func(plain string) []byte {
		var b bytes.Buffer
		zw := gzip.NewWriter(&b)
		zw.Write([]byte(plain))
		zw.Close()
		return mustEncrypt(t, c, b.Bytes())
	}
	build := func(header, content string) []byte {
		var b bytes.Buffer
		b.WriteString(magic)
		binary.Write(&b, binary.BigEndian, uint32(len(header)))
		b.WriteString(header)
		b.WriteString(content)
		return sealRaw(b.String())
	}
	const written = `"written":"2026-01-01T00:00:00Z"`
	cases := []struct {
		name string
		obj  []byte
		want string // part of the error that proves the right check fired
	}{
		{"not gzip", mustEncrypt(t, c, []byte("plain text")), "gzip"},
		{"wrong magic", sealRaw("something-else/1\n"), "unknown format"},
		{"truncated", sealRaw(magic + "\x00"), "truncated"},
		{"huge header", sealRaw(magic + "\xff\xff\xff\xff"), "bad header length"},
		{"bad hash", build(`{"kind":"file","key":"k","sha256":"00","size":4,`+written+`}`, "data"), "does not match its header"},
		{"wrong size", build(`{"kind":"file","key":"k","sha256":"`+Hash([]byte("data"))+`","size":5,`+written+`}`, "data"), "does not match its header"},
		{"unknown kind", build(`{"kind":"dir","key":"k",`+written+`}`, ""), "unknown kind"},
		{"link with body", build(`{"kind":"link","key":"k",`+written+`}`, "xx"), "unexpected content"},
	}
	for _, tc := range cases {
		_, _, err := Open(c, "k", tc.obj)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: Open error = %v, want one mentioning %q", tc.name, err, tc.want)
		}
	}
}

func TestSealRejectsOversizedContent(t *testing.T) {
	c := newCipher(t)
	big := []byte(strings.Repeat("x", MaxContentSize+1))
	if _, err := Seal(c, Header{Kind: KindFile, Key: "big"}, big); err == nil {
		t.Error("Seal accepted content over the size limit")
	}
}

func mustEncrypt(t *testing.T, c Cipher, b []byte) []byte {
	t.Helper()
	out, err := c.Encrypt(b)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
