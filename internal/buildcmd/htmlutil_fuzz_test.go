package buildcmd

import (
	"strings"
	"testing"
)

// TestAddOrReplaceAttr_RoundTrip pins the documented behaviour: setting an
// attribute either replaces an existing value or inserts a new pair before
// the closing `>`, and the resulting tag MUST contain the new value.
func TestAddOrReplaceAttrRoundTrip(t *testing.T) {
	cases := []struct {
		name     string
		tag      string
		attr     string
		value    string
		wantHave string // substring that must appear
	}{
		{"replace existing", `<img src="old.png" alt="x">`, "src", "new.png", `src="new.png"`},
		{"insert new", `<img alt="x">`, "src", "new.png", `src="new.png"`},
		{"case-insensitive match", `<IMG SRC="old.png">`, "src", "new.png", `src="new.png"`},
		{"single-quoted existing", `<img src='old.png'>`, "src", "new.png", `src="new.png"`},
		{"self-closing tag", `<img src="old.png"/>`, "src", "new.png", `src="new.png"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := addOrReplaceAttr(tc.tag, tc.attr, tc.value)
			if !strings.Contains(got, tc.wantHave) {
				t.Errorf("addOrReplaceAttr(%q, %q, %q) = %q, want substring %q",
					tc.tag, tc.attr, tc.value, got, tc.wantHave)
			}
		})
	}
}

// FuzzAddOrReplaceAttr_Idempotent is the core regex-safety contract: applying
// the transform twice with the same arguments must equal applying it once.
// The recon flagged regex-based HTML manipulation as untested; this fuzz
// surfaces inputs where the regex either over-matches, leaves a stale
// attribute behind, or duplicates the new pair.
func FuzzAddOrReplaceAttrIdempotent(f *testing.F) {
	for _, seed := range []struct {
		tag, attr, value string
	}{
		{`<img src="a.png">`, "src", "b.png"},
		{`<img>`, "alt", "hello"},
		{`<a href="/x">x</a>`, "href", "/y"},
		{`<img src="a.png" alt="x">`, "alt", ""},
		{`<img>`, "", ""},
	} {
		f.Add(seed.tag, seed.attr, seed.value)
	}

	f.Fuzz(func(t *testing.T, tag, attr, value string) {
		// The transform's contract only applies to non-empty attr names.
		// Empty attr makes the underlying regex match nothing meaningful;
		// behavior is technically defined (insert " ="" ") but not useful,
		// so we skip — the contract under test is about realistic inputs.
		if attr == "" {
			return
		}
		// Inputs containing quotes or backslashes can produce outputs that
		// re-feed into the regex in unexpected ways. The hard invariant we
		// assert is just idempotency, which is the property regex bugs
		// most commonly break.
		once := addOrReplaceAttr(tag, attr, value)
		twice := addOrReplaceAttr(once, attr, value)
		if once != twice {
			t.Fatalf("addOrReplaceAttr not idempotent:\n  tag=%q attr=%q value=%q\n  once = %q\n  twice= %q",
				tag, attr, value, once, twice)
		}
	})
}

// TestGetTagAttr_HappyAndMissing pins the read side: an attribute that exists
// returns its quoted value; a missing one returns the empty string. This is
// what the larger transform pipeline relies on when deciding whether to
// rewrite, inline, or skip an asset reference.
func TestGetTagAttrHappyAndMissing(t *testing.T) {
	cases := []struct {
		tag, attr, want string
	}{
		{`<img src="hero.webp" alt="x">`, "src", "hero.webp"},
		{`<img SRC="x.png">`, "src", "x.png"},
		{`<a href='/path' target="_blank">`, "href", "/path"},
		{`<img alt="x">`, "src", ""},
		{`<img>`, "src", ""},
	}
	for _, tc := range cases {
		got := getTagAttr(tc.tag, tc.attr)
		if got != tc.want {
			t.Errorf("getTagAttr(%q, %q) = %q, want %q", tc.tag, tc.attr, got, tc.want)
		}
	}
}

// TestIsExternalRef_AdditionalCases extends the existing TestIsExternalRef
// in fingerprint_test.go with a few corner cases that surfaced while
// reviewing transform behaviour: scheme-relative URLs (//host) and
// whitespace-then-HTTPS strings produced by sloppy templates.
func TestIsExternalRefAdditionalCases(t *testing.T) {
	external := []string{
		"https://cdn.example.com/a.css",
		"http://example.com/x",
		"//example.com/x",
		"  HTTPS://example.com/X",
	}
	local := []string{
		"/assets/x.css",
		"x.css",
		"./x.css",
		"../x.css",
		"",
	}
	for _, ref := range external {
		if !isExternalRef(ref) {
			t.Errorf("isExternalRef(%q) = false, want true", ref)
		}
	}
	for _, ref := range local {
		if isExternalRef(ref) {
			t.Errorf("isExternalRef(%q) = true, want false", ref)
		}
	}
}
