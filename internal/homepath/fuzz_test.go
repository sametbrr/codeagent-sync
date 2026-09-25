package homepath

import (
	"strings"
	"testing"
)

var fuzzSeeds = []string{
	"/Users/samet/.claude/x",
	`{"p":"C:\\Users\\ad\\x y\\z\n"}`,
	`C:\Users\ad C:/Users/ad /c/Users/ad`,
	"/Users/samet/Users/samet",
	"x/Users/samet /Users/samet2",
	`c:\USERS\ad\`,
}

// On a POSIX machine, content that does not contain a placeholder must come
// back unchanged after a trip through the portable form.
func FuzzPOSIXRoundTrip(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if strings.Contains(s, placeholderPrefix) {
			t.Skip()
		}
		if got := string(mac.ToLocal(mac.ToPortable([]byte(s)))); got != s {
			t.Fatalf("round trip of %q gave %q", s, got)
		}
	})
}

// Expanding and translating again must not change the portable form. On
// Windows the local spelling can change (c:\users -> C:\Users), so this is the
// property that keeps two machines from rewriting each other's files.
func FuzzPortableIsStable(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if strings.Contains(s, placeholderPrefix) {
			t.Skip()
		}
		for _, m := range []*Mapper{mac, linux, win} {
			p1 := m.ToPortable([]byte(s))
			p2 := m.ToPortable(m.ToLocal(p1))
			if string(p1) != string(p2) {
				t.Fatalf("unstable portable form for %q: %q then %q", s, p1, p2)
			}
		}
	})
}
