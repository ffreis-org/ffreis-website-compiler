package buildcmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ffreis-website-compiler/internal/testutil"
)

// ── injectNavigationEnhancements ──────────────────────────────────────────────

func TestInjectNavigationEnhancements_AddsViewTransitionStyle(t *testing.T) {
	html := `<html><head><title>X</title></head><body></body></html>`
	got := injectNavigationEnhancements(html)
	if !strings.Contains(got, "@view-transition") {
		t.Errorf("expected view-transition style injected, got: %s", got)
	}
}

func TestInjectNavigationEnhancements_AddsSpeculationRulesScript(t *testing.T) {
	html := `<html><head></head><body></body></html>`
	got := injectNavigationEnhancements(html)
	if !strings.Contains(got, "speculationrules") {
		t.Errorf("expected speculationrules script injected, got: %s", got)
	}
}

func TestInjectNavigationEnhancements_InjectedBeforeHeadClose(t *testing.T) {
	html := `<html><head></head><body></body></html>`
	got := injectNavigationEnhancements(html)
	headCloseIdx := strings.Index(got, "</head>")
	specIdx := strings.Index(got, "speculationrules")
	if specIdx > headCloseIdx {
		t.Errorf("expected speculations rules before </head>; headClose=%d specIdx=%d", headCloseIdx, specIdx)
	}
}

func TestInjectNavigationEnhancements_NoHeadTagIsNoOp(t *testing.T) {
	// If there's no </head>, nothing should be injected (no panic, no modification).
	html := `<html><body><p>hello</p></body></html>`
	got := injectNavigationEnhancements(html)
	if got != html {
		t.Errorf("expected no change when no </head>, got: %s", got)
	}
}

func TestInjectNavigationEnhancements_InjectedExactlyOnce(t *testing.T) {
	html := `<html><head></head><body></body></html>`
	got := injectNavigationEnhancements(html)
	if strings.Count(got, "speculationrules") != 1 {
		t.Errorf("expected speculationrules injected exactly once, got: %s", got)
	}
}

// ── transformStylesheets ──────────────────────────────────────────────────────

func makeStylesheetDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		testutil.MustMkdirAll(t, filepath.Dir(p))
		testutil.WriteFiles(t, map[string]string{p: content})
	}
	return dir
}

func TestTransformStylesheets_HeadLinkBecomesStyleBlock(t *testing.T) {
	dir := makeStylesheetDir(t, map[string]string{"css/main.css": "body{color:red}"})
	html := `<html><head><link rel="stylesheet" href="/css/main.css"></head><body></body></html>`
	got, err := transformStylesheets(html, dir, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, `<link rel="stylesheet"`) {
		t.Error("expected head <link> replaced by <style>")
	}
	if !strings.Contains(got, "<style>") {
		t.Error("expected <style> block in output")
	}
	// Minified CSS should appear.
	if !strings.Contains(got, "color:red") {
		t.Error("expected CSS content inlined")
	}
}

func TestTransformStylesheets_BodyLinkGetsDeferredPattern(t *testing.T) {
	dir := makeStylesheetDir(t, map[string]string{"css/widget.css": ".w{}"})
	html := `<html><head></head><body><link rel="stylesheet" href="/css/widget.css"><p>hi</p></body></html>`
	got, err := transformStylesheets(html, dir, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, `media="print"`) {
		t.Error("expected deferred body CSS with media=print")
	}
	if !strings.Contains(got, `onload="this.media='all'"`) {
		t.Error("expected onload handler in deferred CSS")
	}
	if !strings.Contains(got, "<noscript>") {
		t.Error("expected noscript fallback for deferred CSS")
	}
}

func TestTransformStylesheets_HeadMediaAttributePreserved(t *testing.T) {
	dir := makeStylesheetDir(t, map[string]string{"css/print.css": "@media print{body{font-size:12pt}}"})
	html := `<html><head><link rel="stylesheet" href="/css/print.css" media="print"></head></html>`
	got, err := transformStylesheets(html, dir, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, `<style media="print">`) {
		t.Errorf("expected <style media=\"print\">, got: %s", got)
	}
}

func TestTransformStylesheets_InlineBodyCSSFlagInlinesBodyLink(t *testing.T) {
	dir := makeStylesheetDir(t, map[string]string{"css/widget.css": ".w{color:blue}"})
	html := `<html><head></head><body><link rel="stylesheet" href="/css/widget.css"></body></html>`
	got, err := transformStylesheets(html, dir, false, true) // inlineBodyCSS=true
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, `media="print"`) {
		t.Error("expected body CSS inlined as <style>, not deferred")
	}
	if !strings.Contains(got, "<style>") {
		t.Error("expected inline <style> block for body CSS")
	}
}

func TestTransformStylesheets_NoHeadTagInlinesEverything(t *testing.T) {
	// If there's no </head>, the entire document is treated as head.
	dir := makeStylesheetDir(t, map[string]string{"css/main.css": "body{}"})
	html := `<html><link rel="stylesheet" href="/css/main.css"><p>content</p></html>`
	got, err := transformStylesheets(html, dir, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, `<link rel="stylesheet"`) {
		t.Error("expected all CSS inlined when no </head>")
	}
}

func TestTransformStylesheets_ExternalStylesheetsUntouched(t *testing.T) {
	dir := t.TempDir()
	html := `<html><head><link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Roboto"></head></html>`
	got, err := transformStylesheets(html, dir, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "fonts.googleapis.com") {
		t.Error("expected external stylesheet left unchanged")
	}
}

func TestTransformStylesheets_EmbedFontsTurnsURLsToDataURIs(t *testing.T) {
	dir := makeStylesheetDir(t, map[string]string{
		"css/main.css":  `@font-face{src:url("/fonts/f.woff2")}`,
		"fonts/f.woff2": "woff2bytes",
	})
	html := `<html><head><link rel="stylesheet" href="/css/main.css"></head></html>`
	got, err := transformStylesheets(html, dir, true, false) // embedFonts=true
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, `"/fonts/f.woff2"`) {
		t.Error("expected font url() replaced with data URI")
	}
	if !strings.Contains(got, "data:font/woff2;base64,") {
		t.Error("expected base64 font data URI in inlined CSS")
	}
}

// ── deferLocalStylesheets ─────────────────────────────────────────────────────

func TestDeferLocalStylesheets_ProducesCorrectDeferredPattern(t *testing.T) {
	dir := makeStylesheetDir(t, map[string]string{"css/widget.css": ".w{}"})
	html := `<div><link rel="stylesheet" href="/css/widget.css"></div>`
	got, err := deferLocalStylesheets(html, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, `media="print"`) {
		t.Errorf("expected media=print in deferred CSS, got: %s", got)
	}
	if !strings.Contains(got, `onload="this.media='all'"`) {
		t.Errorf("expected onload in deferred CSS, got: %s", got)
	}
	if !strings.Contains(got, `<noscript><link rel="stylesheet" href="/css/widget.css"></noscript>`) {
		t.Errorf("expected noscript fallback with original href, got: %s", got)
	}
}

func TestDeferLocalStylesheets_MissingFileLeftAsIs(t *testing.T) {
	dir := t.TempDir()
	html := `<link rel="stylesheet" href="/css/ghost.css">`
	got, err := deferLocalStylesheets(html, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Missing file should not get the deferred treatment (left unchanged).
	if strings.Contains(got, `media="print"`) {
		t.Error("expected missing CSS file not deferred")
	}
}

func TestDeferLocalStylesheets_ExternalLeftUntouched(t *testing.T) {
	dir := t.TempDir()
	html := `<link rel="stylesheet" href="https://cdn.example.com/style.css">`
	got, err := deferLocalStylesheets(html, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "cdn.example.com") {
		t.Error("expected external stylesheet unchanged")
	}
}

// ── addOrReplaceAttr ──────────────────────────────────────────────────────────

func TestAddOrReplaceAttr_ReplacesExistingValue(t *testing.T) {
	tag := `<link rel="stylesheet" href="/css/main.css" media="all">`
	got := addOrReplaceAttr(tag, "media", "print")
	if !strings.Contains(got, `media="print"`) {
		t.Errorf("expected media=print, got: %s", got)
	}
	if strings.Contains(got, `media="all"`) {
		t.Errorf("expected old media=all removed, got: %s", got)
	}
}

func TestAddOrReplaceAttr_AddsWhenAbsent(t *testing.T) {
	tag := `<link rel="stylesheet" href="/css/main.css">`
	got := addOrReplaceAttr(tag, "media", "print")
	if !strings.Contains(got, `media="print"`) {
		t.Errorf("expected media=print added, got: %s", got)
	}
}

func TestAddOrReplaceAttr_ReplacesOnloadValue(t *testing.T) {
	tag := `<link rel="stylesheet" href="/x.css" media="print" onload="old()">`
	got := addOrReplaceAttr(tag, "onload", "this.media='all'")
	if !strings.Contains(got, `onload="this.media='all'"`) {
		t.Errorf("expected onload replaced, got: %s", got)
	}
}

// ── resolveCSSAssetRef ────────────────────────────────────────────────────────

func TestResolveCSSAssetRef_AbsoluteRefStripsLeadingSlash(t *testing.T) {
	got := resolveCSSAssetRef("css/main.css", "/fonts/inter.woff2")
	if got != "fonts/inter.woff2" {
		t.Errorf("expected fonts/inter.woff2, got %q", got)
	}
}

func TestResolveCSSAssetRef_RelativeRefResolvedFromCSSDir(t *testing.T) {
	got := resolveCSSAssetRef("css/components/button.css", "../images/bg.png")
	if got != "css/images/bg.png" {
		t.Errorf("expected css/images/bg.png, got %q", got)
	}
}

func TestResolveCSSAssetRef_SameDirRelative(t *testing.T) {
	got := resolveCSSAssetRef("css/main.css", "fonts/inter.woff2")
	if got != "css/fonts/inter.woff2" {
		t.Errorf("expected css/fonts/inter.woff2, got %q", got)
	}
}

func TestResolveCSSAssetRef_RootCSSFile(t *testing.T) {
	// CSS file at the root level.
	got := resolveCSSAssetRef("main.css", "fonts/x.woff2")
	if got != "fonts/x.woff2" {
		t.Errorf("expected fonts/x.woff2, got %q", got)
	}
}

// ── resolvePageTarget ─────────────────────────────────────────────────────────

func TestResolvePageTarget_NormalURL(t *testing.T) {
	outDir := t.TempDir()
	path, err := resolvePageTarget(outDir, "about", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(path, "about.html") {
		t.Errorf("expected about.html, got %q", path)
	}
}

func TestResolvePageTarget_CleanURLs(t *testing.T) {
	outDir := t.TempDir()
	path, err := resolvePageTarget(outDir, "about", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(path, filepath.Join("about", "index.html")) {
		t.Errorf("expected about/index.html, got %q", path)
	}
}

func TestResolvePageTarget_IndexPageCleanURLs(t *testing.T) {
	outDir := t.TempDir()
	path, err := resolvePageTarget(outDir, "index", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(path, "index.html") {
		t.Errorf("expected index.html, got %q", path)
	}
}

// ── inlineLocalAssets (full -inline-assets mode) ──────────────────────────────

func TestInlineLocalAssets_InlinesCSSWithDataURIFonts(t *testing.T) {
	dir := makeStylesheetDir(t, map[string]string{
		"css/main.css":  `@font-face{src:url("/fonts/f.woff2")}`,
		"fonts/f.woff2": "fontdata",
	})
	html := `<html><head><link rel="stylesheet" href="/css/main.css"></head></html>`
	got, err := inlineLocalAssets(html, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// In full inline mode, fonts must be data URIs.
	if strings.Contains(got, `url("/fonts/f.woff2")`) {
		t.Error("expected font url() embedded as data URI in full inline mode")
	}
	if !strings.Contains(got, "data:font/woff2;base64,") {
		t.Error("expected base64 font data URI in full inline mode")
	}
}

func TestInlineLocalAssets_InlinesScript(t *testing.T) {
	dir := makeStylesheetDir(t, map[string]string{
		"css/main.css": "body{}",
		"js/app.js":    `alert("hello");`,
	})
	html := `<html><head><link rel="stylesheet" href="/css/main.css"></head>` +
		`<body><script src="/js/app.js"></script></body></html>`
	got, err := inlineLocalAssets(html, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, `src="/js/app.js"`) {
		t.Error("expected script src inlined in full inline mode")
	}
	if !strings.Contains(got, `alert("hello")`) {
		t.Error("expected script content present")
	}
}

func TestInlineLocalAssets_InlinesImage(t *testing.T) {
	dir := makeStylesheetDir(t, map[string]string{"css/main.css": "body{}"})
	mustWriteBytes(t, filepath.Join(dir, "images", "logo.png"), minimalPNG)
	html := `<html><head><link rel="stylesheet" href="/css/main.css"></head>` +
		`<body><img src="/images/logo.png" alt="logo"></body></html>`
	got, err := inlineLocalAssets(html, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, `src="/images/logo.png"`) {
		t.Error("expected image inlined as data URI in full inline mode")
	}
	if !strings.Contains(got, "data:image/png;base64,") {
		t.Error("expected base64 PNG data URI")
	}
}

// ── clean URLs integration ────────────────────────────────────────────────────

func TestRun_CleanURLsProducesIndexHTML(t *testing.T) {
	websiteRoot := newTestWebsiteRoot(t)
	testutil.WriteFiles(t, map[string]string{
		filepath.Join(websiteRoot, "src", "assets", "css", fileMainCSS):           mainCSSContent,
		filepath.Join(websiteRoot, "src", "data", fileSiteContractYAML):           "",
		filepath.Join(websiteRoot, "src", "templates", "pages", fileAgendaGoHTML): `{{define "page"}}<p>hello</p>{{end}}`,
	})

	outDir := t.TempDir()
	args := []string{
		flagWebsiteRoot, websiteRoot,
		flagOut, outDir,
		"-clean-urls",
	}
	if err := Run(args, testutil.DiscardLogger()); err != nil {
		t.Fatalf(buildRunFailed, err)
	}

	// agenda page must be at agenda/index.html, not agenda.html.
	agendaIndexPath := filepath.Join(outDir, "agenda", "index.html")
	if _, err := os.Stat(agendaIndexPath); err != nil {
		t.Fatalf("expected %s to exist: %v", agendaIndexPath, err)
	}
	// The old .html path must not exist.
	if _, err := os.Stat(filepath.Join(outDir, "agenda.html")); !os.IsNotExist(err) {
		t.Fatalf("expected agenda.html absent with clean-urls, got err=%v", err)
	}
}

// ── pipeline ordering: LQIP + fingerprint ────────────────────────────────────

func TestRun_LQIPDataSrcIsFingerprinted(t *testing.T) {
	// This test verifies that the LQIP data-src gets a fingerprinted URL,
	// while the LQIP src (data URI) is left as-is.
	websiteRoot := newTestWebsiteRoot(t)
	testutil.MustMkdirAll(t, filepath.Join(websiteRoot, "src", "assets", "images"))
	writePNG(t, filepath.Join(websiteRoot, "src", "assets"), "images/hero.png", 100, 50)
	testutil.WriteFiles(t, map[string]string{
		filepath.Join(websiteRoot, "src", "assets", "css", fileMainCSS):           mainCSSContent,
		filepath.Join(websiteRoot, "src", "data", fileSiteContractYAML):           "",
		filepath.Join(websiteRoot, "src", "templates", "pages", fileAgendaGoHTML): `{{define "page"}}<img src="/images/hero.png" loading="eager" alt="hero">{{end}}`,
	})

	outDir := t.TempDir()
	if err := Run([]string{flagWebsiteRoot, websiteRoot, flagOut, outDir}, testutil.DiscardLogger()); err != nil {
		t.Fatalf(buildRunFailed, err)
	}

	html := string(mustReadFile(t, filepath.Join(outDir, fileAgendaHTML)))
	// The src must be a LQIP data URI.
	if !strings.Contains(html, `src="data:image/jpeg;base64,`) {
		t.Error("expected LQIP data URI in src")
	}
	// The data-src must be a fingerprinted path, not the original.
	if strings.Contains(html, `data-src="/images/hero.png"`) {
		t.Error("expected data-src to be fingerprinted (hashed), not original path")
	}
	if !strings.Contains(html, "data-src=\"/images/hero.") {
		t.Error("expected data-src to contain fingerprinted image path")
	}
}

// ── favicon & robots.txt ─────────────────────────────────────────────────────

func TestRun_FaviconAndRobotsCopiedToRoot(t *testing.T) {
	websiteRoot := newTestWebsiteRoot(t)
	testutil.WriteFiles(t, map[string]string{
		filepath.Join(websiteRoot, "src", "assets", "css", fileMainCSS):           mainCSSContent,
		filepath.Join(websiteRoot, "src", "assets", "favicon.ico"):                "icodata",
		filepath.Join(websiteRoot, "src", "assets", "robots.txt"):                 "User-agent: *\nDisallow:",
		filepath.Join(websiteRoot, "src", "data", fileSiteContractYAML):           "",
		filepath.Join(websiteRoot, "src", "templates", "pages", fileAgendaGoHTML): `{{define "page"}}<p>ok</p>{{end}}`,
	})

	outDir := t.TempDir()
	if err := Run([]string{flagWebsiteRoot, websiteRoot, flagOut, outDir}, testutil.DiscardLogger()); err != nil {
		t.Fatalf(buildRunFailed, err)
	}

	mustStat(t, filepath.Join(outDir, "favicon.ico"))
	mustStat(t, filepath.Join(outDir, "robots.txt"))
}

// ── CSS url() path rewriting in head (integration) ────────────────────────────

func TestRun_HeadCSSURLRewrittenToRootRelative(t *testing.T) {
	websiteRoot := newTestWebsiteRoot(t)
	testutil.MustMkdirAll(t, filepath.Join(websiteRoot, "src", "assets", "fonts"))
	testutil.WriteFiles(t, map[string]string{
		filepath.Join(websiteRoot, "src", "assets", "fonts", "f.woff2"):           "fontdata",
		filepath.Join(websiteRoot, "src", "assets", "css", fileMainCSS):           `@font-face{src:url("../fonts/f.woff2")}`,
		filepath.Join(websiteRoot, "src", "data", fileSiteContractYAML):           "",
		filepath.Join(websiteRoot, "src", "templates", "pages", fileAgendaGoHTML): `{{define "page"}}<p>ok</p>{{end}}`,
	})

	outDir := t.TempDir()
	if err := Run([]string{flagWebsiteRoot, websiteRoot, flagOut, outDir}, testutil.DiscardLogger()); err != nil {
		t.Fatalf(buildRunFailed, err)
	}

	html := string(mustReadFile(t, filepath.Join(outDir, fileAgendaHTML)))
	// The url() should be root-relative and fingerprinted, not relative to the CSS file.
	if strings.Contains(html, `url("../fonts/f.woff2")`) {
		t.Error("expected relative url() resolved to root-relative path")
	}
	if !strings.Contains(html, `url("/fonts/f.`) {
		t.Errorf("expected root-relative fingerprinted url in inlined CSS, got: %s", html)
	}
}

// ── copy-assets=false ────────────────────────────────────────────────────────

func TestRun_CopyAssetsFalseSkipsAllAssets(t *testing.T) {
	websiteRoot := newTestWebsiteRoot(t)
	testutil.WriteFiles(t, map[string]string{
		filepath.Join(websiteRoot, "src", "assets", "css", fileMainCSS):           mainCSSContent,
		filepath.Join(websiteRoot, "src", "data", fileSiteContractYAML):           "",
		filepath.Join(websiteRoot, "src", "templates", "pages", fileAgendaGoHTML): `{{define "page"}}<p>ok</p>{{end}}`,
	})

	outDir := t.TempDir()
	args := []string{
		flagWebsiteRoot, websiteRoot,
		flagOut, outDir,
		"-copy-assets=false",
	}
	if err := Run(args, testutil.DiscardLogger()); err != nil {
		t.Fatalf(buildRunFailed, err)
	}

	// With copy-assets=false nothing should be in dist except the HTML.
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatalf("reading outDir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	for _, name := range names {
		if name != "agenda.html" && name != "sitemap.xml" {
			t.Errorf("unexpected file in dist with copy-assets=false: %s", name)
		}
	}
}

// ── multiple pages (fingerprint cross-page consistency) ───────────────────────

func TestRun_SameAssetFingerprintedConsistentlyAcrossPages(t *testing.T) {
	websiteRoot := newTestWebsiteRoot(t)
	testutil.MustMkdirAll(t, filepath.Join(websiteRoot, "src", "assets", "images"))
	mustWriteBytes(t, filepath.Join(websiteRoot, "src", "assets", "images", "logo.png"), minimalPNG)

	const logoTag = `<img src="/images/logo.png" alt="logo">`
	testutil.WriteFiles(t, map[string]string{
		filepath.Join(websiteRoot, "src", "assets", "css", fileMainCSS):           mainCSSContent,
		filepath.Join(websiteRoot, "src", "data", fileSiteContractYAML):           "",
		filepath.Join(websiteRoot, "src", "templates", "pages", "index.gohtml"):   `{{define "page"}}` + logoTag + `{{end}}`,
		filepath.Join(websiteRoot, "src", "templates", "pages", fileAgendaGoHTML): `{{define "page"}}` + logoTag + `{{end}}`,
	})

	outDir := t.TempDir()
	if err := Run([]string{flagWebsiteRoot, websiteRoot, flagOut, outDir}, testutil.DiscardLogger()); err != nil {
		t.Fatalf(buildRunFailed, err)
	}

	extractImgSrc := func(html string) string {
		for _, line := range strings.Split(html, "\n") {
			if strings.Contains(line, "logo.") && strings.Contains(line, "<img") {
				start := strings.Index(line, `src="`) + 5
				end := strings.Index(line[start:], `"`)
				if start > 4 && end > 0 {
					return line[start : start+end]
				}
			}
		}
		return ""
	}

	indexHTML := string(mustReadFile(t, filepath.Join(outDir, "index.html")))
	agendaHTML := string(mustReadFile(t, filepath.Join(outDir, "agenda.html")))
	srcIndex := extractImgSrc(indexHTML)
	srcAgenda := extractImgSrc(agendaHTML)

	if srcIndex == "" || srcAgenda == "" {
		t.Fatalf("could not extract img src from pages: index=%q agenda=%q", srcIndex, srcAgenda)
	}
	if srcIndex != srcAgenda {
		t.Errorf("expected same fingerprinted path on both pages, got:\n  index:  %q\n  agenda: %q", srcIndex, srcAgenda)
	}
}
