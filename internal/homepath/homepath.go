// Package homepath makes file content portable between machines by replacing
// the user's home directory with a placeholder when content is uploaded and
// expanding the placeholder again when it is downloaded.
//
// The home directory is recognized in every spelling tools are likely to
// write: POSIX (/Users/ad, /home/ad) and, on Windows, the raw form
// (C:\Users\ad), the escaped form found in JSON and TOML strings
// (C:\\Users\\ad), the forward-slash form (C:/Users/ad) and the Git Bash form
// (/c/Users/ad). A match must be a whole path component: /Users/ad never
// matches inside /Users/adam or /Volumes/x/Users/ad.
package homepath

import (
	"bytes"
	"sort"
	"strings"

	"github.com/sametbrr/codeagent-sync/internal/platform"
)

// Placeholder stands for the home directory in synced content. It is
// deliberately not "$HOME" or "${HOME}": scripts and hook commands use those
// literally and must reach other machines unchanged.
//
// Windows paths written with backslashes get placeholders of their own, so
// that such text returns to Windows exactly as it was written (cmd.exe does
// not accept forward slashes everywhere) while POSIX machines receive it with
// forward slashes.
const (
	Placeholder           = "__CODEAGENT_HOME__"
	placeholderWin        = "__CODEAGENT_HOME_WIN__"         // C:\Users\ad
	placeholderWinEscaped = "__CODEAGENT_HOME_WIN_ESCAPED__" // C:\\Users\\ad
)

const placeholderPrefix = "__CODEAGENT_HOME"

type spelling int

const (
	slashSpelling  spelling = iota // /Users/ad, C:/Users/ad, /c/Users/ad
	winSpelling                    // C:\Users\ad
	winEscSpelling                 // C:\\Users\\ad
)

var placeholders = [...]string{
	slashSpelling:  Placeholder,
	winSpelling:    placeholderWin,
	winEscSpelling: placeholderWinEscaped,
}

// Mapper translates between one machine's home directory and the
// placeholders.
type Mapper struct {
	windows bool
	forms   []form    // spellings of the home directory recognized locally
	expand  [3][]byte // what each placeholder expands to on this machine
}

type form struct {
	text     []byte
	fold     bool // ASCII case-insensitive match, for Windows paths
	spelling spelling
}

// New returns a Mapper for a machine with the given home directory. aliases
// are other absolute spellings of the same directory, such as the target of a
// symlinked home (/home -> /var/home on Fedora Silverblue).
func New(home string, os platform.OS, aliases ...string) *Mapper {
	m := &Mapper{windows: os == platform.Windows}
	for _, h := range append([]string{home}, aliases...) {
		if m.windows {
			m.forms = append(m.forms, windowsForms(h)...)
		} else if h = strings.TrimRight(h, "/"); h != "" {
			m.forms = append(m.forms, form{text: []byte(h), spelling: slashSpelling})
		}
	}
	// Longest first, so a spelling that contains another one wins.
	sort.SliceStable(m.forms, func(i, j int) bool {
		return len(m.forms[i].text) > len(m.forms[j].text)
	})

	if m.windows {
		raw := windowsRaw(home)
		m.expand[slashSpelling] = []byte(strings.ReplaceAll(raw, `\`, "/"))
		m.expand[winSpelling] = []byte(raw)
		m.expand[winEscSpelling] = []byte(strings.ReplaceAll(raw, `\`, `\\`))
	} else {
		h := []byte(strings.TrimRight(home, "/"))
		m.expand = [3][]byte{h, h, h}
	}
	return m
}

func windowsRaw(home string) string {
	return strings.TrimRight(strings.ReplaceAll(home, "/", `\`), `\`)
}

func windowsForms(home string) []form {
	raw := windowsRaw(home)
	if raw == "" {
		return nil
	}
	forms := []form{
		{text: []byte(raw), fold: true, spelling: winSpelling},
		{text: []byte(strings.ReplaceAll(raw, `\`, `\\`)), fold: true, spelling: winEscSpelling},
		{text: []byte(strings.ReplaceAll(raw, `\`, "/")), fold: true, spelling: slashSpelling},
	}
	// Git Bash spells C:\Users\ad as /c/Users/ad.
	if len(raw) >= 3 && raw[1] == ':' && raw[2] == '\\' && isLetter(raw[0]) {
		gitBash := "/" + strings.ToLower(raw[:1]) + strings.ReplaceAll(raw[2:], `\`, "/")
		forms = append(forms, form{text: []byte(gitBash), fold: true, spelling: slashSpelling})
	}
	return forms
}

// ToPortable replaces this machine's home directory with placeholders.
func (m *Mapper) ToPortable(data []byte) []byte {
	for _, f := range m.forms {
		data = f.replace(data)
	}
	return data
}

// ToLocal expands the placeholders to this machine's home directory. On a
// POSIX machine the rest of a path that was written on Windows gets forward
// slashes, so that it resolves there.
func (m *Mapper) ToLocal(data []byte) []byte {
	var out []byte
	copied := 0 // data[:copied] is already in out
	for from := 0; ; {
		i := bytes.Index(data[from:], []byte(placeholderPrefix))
		if i < 0 {
			break
		}
		i += from
		sp, n, ok := placeholderAt(data[i:])
		if !ok {
			from = i + 1
			continue
		}
		out = append(out, data[copied:i]...)
		out = append(out, m.expand[sp]...)
		next := i + n
		if !m.windows && sp != slashSpelling {
			out, next = appendPOSIXTail(out, data, next, sp)
		}
		copied, from = next, next
	}
	if out == nil {
		return data
	}
	return append(out, data[copied:]...)
}

// IsText reports whether data looks like text. Translation only applies to
// text; binary files (a NUL byte in the first 8 KiB) are synced byte for byte.
func IsText(data []byte) bool {
	return bytes.IndexByte(data[:min(len(data), 8192)], 0) < 0
}

func placeholderAt(b []byte) (spelling, int, bool) {
	for _, sp := range []spelling{winEscSpelling, winSpelling, slashSpelling} {
		if bytes.HasPrefix(b, []byte(placeholders[sp])) {
			return sp, len(placeholders[sp]), true
		}
	}
	return 0, 0, false
}

// replace substitutes the form's placeholder for every occurrence of the form
// that is a whole path component.
func (f form) replace(data []byte) []byte {
	var out []byte
	copied := 0 // data[:copied] is already in out
	for from := 0; ; {
		i := f.index(data, from)
		if i < 0 {
			break
		}
		end := i + len(f.text)
		if (i > 0 && isNameByte(data[i-1])) || (end < len(data) && isNameByte(data[end])) {
			from = i + 1
			continue
		}
		out = append(out, data[copied:i]...)
		out = append(out, placeholders[f.spelling]...)
		copied, from = end, end
	}
	if out == nil {
		return data
	}
	return append(out, data[copied:]...)
}

func (f form) index(data []byte, from int) int {
	if !f.fold {
		i := bytes.Index(data[from:], f.text)
		if i < 0 {
			return -1
		}
		return from + i
	}
	for i := from; i+len(f.text) <= len(data); i++ {
		if equalFoldASCII(data[i:i+len(f.text)], f.text) {
			return i
		}
	}
	return -1
}

// appendPOSIXTail copies the rest of a Windows path that follows the home
// directory, with its separators turned into forward slashes. The path ends
// at the first character that cannot belong to it: a quote, a control
// character and, for a raw path, a space; for an escaped path also a lone
// backslash, which starts an escape sequence such as \" or \n.
func appendPOSIXTail(out, data []byte, i int, sp spelling) ([]byte, int) {
	sep := []byte(`\`)
	if sp == winEscSpelling {
		sep = []byte(`\\`)
	}
	for i < len(data) {
		if bytes.HasPrefix(data[i:], sep) {
			out = append(out, '/')
			i += len(sep)
			continue
		}
		c := data[i]
		if c == '\\' || isPathEnd(c, sp == winSpelling) {
			break
		}
		out = append(out, c)
		i++
	}
	return out, i
}

func isPathEnd(c byte, stopAtSpace bool) bool {
	switch c {
	case '"', '\'', '`', '<', '>', '|', '*', '?', ',', ';', '\n', '\r', '\t', 0:
		return true
	case ' ':
		return stopAtSpace
	}
	return false
}

// isNameByte reports whether c can continue a file name, so a home directory
// followed or preceded by it is part of a longer name. Bytes of multi-byte
// UTF-8 characters count as name bytes.
func isNameByte(c byte) bool {
	return isLetter(c) || (c >= '0' && c <= '9') || c == '_' || c == '.' || c == '-' || c >= 0x80
}

func isLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

func equalFoldASCII(a, b []byte) bool {
	for i := range a {
		if lowerASCII(a[i]) != lowerASCII(b[i]) {
			return false
		}
	}
	return true
}

func lowerASCII(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}
