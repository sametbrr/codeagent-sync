package platform

import (
	"reflect"
	"testing"
)

func TestValidateRelPath(t *testing.T) {
	all := []OS{Darwin, Linux, Windows}
	posix := []OS{Darwin, Linux}

	tests := []struct {
		rel     string
		validOn []OS
	}{
		{"skills/foo/SKILL.md", all},
		{".DS_Store", all},
		{"skills/CONSOLE.md", all},
		{"skills/COM10", all},
		{"skills/con-fig/SKILL.md", all},
		{"look-again/2026-09-24-plan.md", all},

		{"", nil},
		{"/etc/passwd", nil},
		{"a//b", nil},
		{"a/./b", nil},
		{"../escape", nil},
		{"skills/..", nil},
		{"a\x00b", nil},

		{"notes/a:b.md", posix},
		{"skills/CON", posix},
		{"skills/nul.txt", posix},
		{"skills/Com1", posix},
		{"logs/lpt9.log", posix},
		{"name.", posix},
		{"name ", posix},
		{"a<b", posix},
		{"what?", posix},
		{"star*", posix},
		{`back\slash`, posix},
		{"tab\tname", posix},
	}

	for _, tc := range tests {
		for _, target := range all {
			want := contains(tc.validOn, target)
			err := ValidateRelPath(tc.rel, target)
			if (err == nil) != want {
				t.Errorf("ValidateRelPath(%q, %s) error = %v, want valid=%v", tc.rel, target, err, want)
			}
		}
	}
}

func contains(list []OS, o OS) bool {
	for _, x := range list {
		if x == o {
			return true
		}
	}
	return false
}

func TestCaseCollisions(t *testing.T) {
	got := CaseCollisions([]string{
		"skills/Foo/SKILL.md",
		"skills/foo/SKILL.md",
		"a/b",
		"A/c",
		"skills/bar/SKILL.md",
	})
	want := [][]string{
		{"A", "a"},
		{"skills/Foo", "skills/foo"},
		{"skills/Foo/SKILL.md", "skills/foo/SKILL.md"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CaseCollisions() = %v, want %v", got, want)
	}

	if got := CaseCollisions([]string{"skills/a/SKILL.md", "skills/b/SKILL.md"}); got != nil {
		t.Errorf("CaseCollisions() = %v, want none", got)
	}
}
