package buildcmd

import (
	"path/filepath"
	"strings"
	"testing"

	"ffreis-website-compiler/internal/testutil"
)

func TestFlattenCSSImports_NoImports(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteFiles(t, map[string]string{
		filepath.Join(dir, "main.css"): "body { color: red; }",
	})
	got, err := flattenCSSImports("body { color: red; }", "main.css", dir, nil, cssInlineOpts{preserveURLs: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "color: red") {
		t.Fatalf("expected content preserved, got %q", got)
	}
}

func TestFlattenCSSImports_InlinesLocalImport(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteFiles(t, map[string]string{
		filepath.Join(dir, "vars.css"): ":root { --color: blue; }",
	})
	css := `@import "vars.css";
body { color: var(--color); }`
	got, err := flattenCSSImports(css, "main.css", dir, nil, cssInlineOpts{preserveURLs: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "--color: blue") {
		t.Fatalf("expected imported content inlined, got %q", got)
	}
	if strings.Contains(got, `@import "vars.css"`) {
		t.Fatalf("expected @import removed, got %q", got)
	}
}

func TestFlattenCSSImports_NestedImports(t *testing.T) {
	dir := t.TempDir()
	testutil.MustMkdirAll(t, filepath.Join(dir, "tokens"))
	testutil.WriteFiles(t, map[string]string{
		filepath.Join(dir, "tokens", "colors.css"):  ":root { --red: #f00; }",
		filepath.Join(dir, "tokens", "spacing.css"): ":root { --gap: 8px; }",
		filepath.Join(dir, "tokens.css"):             `@import "tokens/colors.css"; @import "tokens/spacing.css";`,
	})
	css := `@import "tokens.css";
body { color: var(--red); gap: var(--gap); }`
	got, err := flattenCSSImports(css, "main.css", dir, nil, cssInlineOpts{preserveURLs: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "--red: #f00") {
		t.Fatalf("expected deep nested colors inlined, got %q", got)
	}
	if !strings.Contains(got, "--gap: 8px") {
		t.Fatalf("expected deep nested spacing inlined, got %q", got)
	}
}

func TestFlattenCSSImports_DeduplicatesSharedImport(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteFiles(t, map[string]string{
		filepath.Join(dir, "shared.css"): ":root { --shared: 1; }",
		filepath.Join(dir, "a.css"):      `@import "shared.css"; .a { color: red; }`,
		filepath.Join(dir, "b.css"):      `@import "shared.css"; .b { color: blue; }`,
	})
	css := `@import "a.css"; @import "b.css";`
	got, err := flattenCSSImports(css, "main.css", dir, nil, cssInlineOpts{preserveURLs: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// shared.css must appear at most once.
	count := strings.Count(got, "--shared: 1")
	if count != 1 {
		t.Fatalf("expected shared import deduplicated (1 occurrence), got %d in: %q", count, got)
	}
}

func TestFlattenCSSImports_CircularImportNoHang(t *testing.T) {
	dir := t.TempDir()
	// a.css imports b.css, b.css imports a.css — must not infinite-loop.
	testutil.WriteFiles(t, map[string]string{
		filepath.Join(dir, "b.css"): `@import "a.css"; .b { color: blue; }`,
	})
	css := `@import "b.css"; .a { color: red; }`
	// Should complete without hanging.
	got, err := flattenCSSImports(css, "a.css", dir, nil, cssInlineOpts{preserveURLs: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == "" {
		t.Fatal("expected some output, got empty string")
	}
}

func TestFlattenCSSImports_ExternalImportLeftVerbatim(t *testing.T) {
	dir := t.TempDir()
	css := `@import url("https://fonts.googleapis.com/css2?family=Roboto");
body { font-family: Roboto; }`
	got, err := flattenCSSImports(css, "main.css", dir, nil, cssInlineOpts{preserveURLs: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "fonts.googleapis.com") {
		t.Fatalf("expected external @import kept verbatim, got %q", got)
	}
}

func TestFlattenCSSImports_MissingFileLeftVerbatim(t *testing.T) {
	dir := t.TempDir()
	css := `@import "nonexistent.css";
body { color: red; }`
	got, err := flattenCSSImports(css, "main.css", dir, nil, cssInlineOpts{preserveURLs: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, `@import "nonexistent.css"`) {
		t.Fatalf("expected missing @import left as-is, got %q", got)
	}
}

func TestFlattenCSSImports_URLRewritingInImportedFile(t *testing.T) {
	dir := t.TempDir()
	testutil.MustMkdirAll(t, filepath.Join(dir, "components"))
	testutil.MustMkdirAll(t, filepath.Join(dir, "images"))
	testutil.WriteFiles(t, map[string]string{
		filepath.Join(dir, "images", "bg.png"):              "png",
		filepath.Join(dir, "components", "button.css"):      `button { background: url("../images/bg.png"); }`,
	})
	css := `@import "components/button.css";`
	got, err := flattenCSSImports(css, "main.css", dir, nil, cssInlineOpts{preserveURLs: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// url() in the imported file should be root-relative, resolved from button.css's location.
	if !strings.Contains(got, `url("/images/bg.png")`) {
		t.Fatalf("expected root-relative url after import, got %q", got)
	}
}
