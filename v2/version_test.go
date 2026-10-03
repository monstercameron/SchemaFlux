package schemaflux

import (
	"regexp"
	"strings"
	"testing"
)

// semanticVersion is the grammar of semver.org, section 2, as its authors
// publish it as a regular expression.
var semanticVersion = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

func TestVersionIsASemanticVersion(t *testing.T) {
	t.Parallel()
	if !semanticVersion.MatchString(Version) {
		t.Fatalf("Version = %q, which is not a semantic version", Version)
	}
	if strings.HasPrefix(Version, "v") {
		t.Errorf("Version = %q: a semantic version has no leading v", Version)
	}
	if !strings.HasPrefix(Version, "2.") {
		t.Errorf("Version = %q: version 2 of the library is a 2.x.y", Version)
	}
}

func TestTheSemanticVersionGrammarAcceptsAndRefusesWhatItShould(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want bool
	}{
		{"2.0.0", true},
		{"2.0.0-dev", true},
		{"2.0.0-rc.1", true},
		{"2.0.0-alpha.beta.1", true},
		{"2.1.3+build.5", true},
		{"2.1.3-dev+build.5", true},
		{"0.0.0", true},
		{"10.20.30", true},
		{"v2.0.0", false},
		{"2.0", false},
		{"2", false},
		{"02.0.0", false},
		{"2.0.0-", false},
		{"2.0.0-01", false},
		{"2.0.0+", false},
		{"2.0.0.1", false},
		{"", false},
		{"two", false},
	}
	for _, c := range cases {
		if got := semanticVersion.MatchString(c.in); got != c.want {
			t.Errorf("semanticVersion.MatchString(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
