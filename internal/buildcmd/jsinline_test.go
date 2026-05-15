package buildcmd

import (
	"path/filepath"
	"strings"
	"testing"

	"ffreis-website-compiler/internal/testutil"
)

func TestInlineSmallLocalScripts_InlinesBelowThreshold(t *testing.T) {
	dir := t.TempDir()
	testutil.MustMkdirAll(t, filepath.Join(dir, "js"))
	testutil.WriteFiles(t, map[string]string{
		filepath.Join(dir, "js", "tiny.js"): `console.log("hi");`,
	})
	doc := `<html><body><script src="/js/tiny.js"></script></body></html>`
	got, err := inlineSmallLocalScripts(doc, dir, 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, `src="/js/tiny.js"`) {
		t.Fatalf("expected script src removed, got %q", got)
	}
	if !strings.Contains(got, `console.log("hi")`) {
		t.Fatalf("expected script content inlined, got %q", got)
	}
}

func TestInlineSmallLocalScripts_KeepsAtThreshold(t *testing.T) {
	dir := t.TempDir()
	testutil.MustMkdirAll(t, filepath.Join(dir, "js"))
	content := strings.Repeat("x", 1024)
	testutil.WriteFiles(t, map[string]string{
		filepath.Join(dir, "js", "big.js"): content,
	})
	doc := `<html><body><script src="/js/big.js"></script></body></html>`
	got, err := inlineSmallLocalScripts(doc, dir, 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, `src="/js/big.js"`) {
		t.Fatalf("expected large script kept external, got %q", got)
	}
}

func TestInlineSmallLocalScripts_SkipsExternalURLs(t *testing.T) {
	dir := t.TempDir()
	doc := `<html><body><script src="https://cdn.example.com/lib.js"></script></body></html>`
	got, err := inlineSmallLocalScripts(doc, dir, 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "cdn.example.com") {
		t.Fatalf("expected external script untouched, got %q", got)
	}
}

func TestInlineSmallLocalScripts_SkipsModuleType(t *testing.T) {
	dir := t.TempDir()
	testutil.MustMkdirAll(t, filepath.Join(dir, "js"))
	testutil.WriteFiles(t, map[string]string{
		filepath.Join(dir, "js", "mod.js"): `export default {};`,
	})
	doc := `<html><body><script type="module" src="/js/mod.js"></script></body></html>`
	got, err := inlineSmallLocalScripts(doc, dir, 1024*1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, `src="/js/mod.js"`) {
		t.Fatalf("expected module script kept external, got %q", got)
	}
}

func TestInlineSmallLocalScripts_ThresholdZeroDisables(t *testing.T) {
	dir := t.TempDir()
	testutil.MustMkdirAll(t, filepath.Join(dir, "js"))
	testutil.WriteFiles(t, map[string]string{
		filepath.Join(dir, "js", "tiny.js"): `alert(1);`,
	})
	doc := `<html><body><script src="/js/tiny.js"></script></body></html>`
	// threshold=0 means disabled: inlineSmallLocalScripts should not be called,
	// but verify directly that 0-byte threshold keeps all scripts external.
	got, err := inlineSmallLocalScripts(doc, dir, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// With threshold 0, every file is >= 0 bytes, so nothing is inlined.
	if !strings.Contains(got, `src="/js/tiny.js"`) {
		t.Fatalf("expected script kept external with threshold 0, got %q", got)
	}
}

func TestRun_InlinesSmallJS(t *testing.T) {
	websiteRoot := newTestWebsiteRoot(t)
	testutil.MustMkdirAll(t, filepath.Join(websiteRoot, "src", "assets", "js"))
	testutil.WriteFiles(t, map[string]string{
		filepath.Join(websiteRoot, "src", "assets", "css", fileMainCSS):           mainCSSContent,
		filepath.Join(websiteRoot, "src", "assets", "js", "theme.js"):             `document.body.classList.add("loaded");`,
		filepath.Join(websiteRoot, "src", "data", fileSiteContractYAML):           "",
		filepath.Join(websiteRoot, "src", "templates", "pages", fileAgendaGoHTML): `{{define "page"}}<p>hello</p>{{end}}`,
		filepath.Join(websiteRoot, "src", "templates", "partials", "head.gohtml"): `{{define "head"}}<link rel="stylesheet" href="/css/main.css"><script src="/js/theme.js"></script>{{end}}`,
	})

	outDir := t.TempDir()
	args := []string{
		flagWebsiteRoot, websiteRoot,
		flagOut, outDir,
		"-js-inline-threshold", "1024",
	}
	if err := Run(args, testutil.DiscardLogger()); err != nil {
		t.Fatalf(buildRunFailed, err)
	}

	html := string(mustReadFile(t, filepath.Join(outDir, fileAgendaHTML)))
	if strings.Contains(html, `src="/js/theme.js"`) {
		t.Fatalf("expected small JS inlined, still has src reference")
	}
	if !strings.Contains(html, `classList.add("loaded")`) {
		t.Fatalf("expected JS content inlined in HTML")
	}
}
