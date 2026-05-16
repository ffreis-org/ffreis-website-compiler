package linkcheck_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ffreis-website-compiler/internal/linkcheck"
)

// writeHTML creates a minimal HTML file with the given anchor hrefs.
func writeHTML(t *testing.T, dir, relPath string, hrefs ...string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	var sb strings.Builder
	sb.WriteString("<!doctype html><html><body>")
	for _, h := range hrefs {
		sb.WriteString(`<a href="` + h + `">link</a>`)
	}
	sb.WriteString("</body></html>")
	if err := os.WriteFile(full, []byte(sb.String()), 0o644); err != nil { //nolint:gosec
		t.Fatalf("write %s: %v", relPath, err)
	}
}

// ── Basic pass / fail ─────────────────────────────────────────────────────────

func TestValidate_PassesWhenAllLinksExist(t *testing.T) {
	d := t.TempDir()
	writeHTML(t, d, "index.html", "/about", "/contact")
	writeHTML(t, d, "about.html")
	writeHTML(t, d, "contact.html")
	if err := linkcheck.ValidateAndReport(d, "", nil); err != nil {
		t.Fatalf("expected pass, got: %v", err)
	}
}

func TestValidate_DetectsBrokenNavLink(t *testing.T) {
	// THE CORE CASE: nav links to /empresa but empresa.html was never generated.
	d := t.TempDir()
	writeHTML(t, d, "index.html", "/about", "/empresa")
	writeHTML(t, d, "about.html")
	// empresa.html intentionally missing

	err := linkcheck.ValidateAndReport(d, "", nil)
	if err == nil {
		t.Fatal("expected error for broken link to /empresa, got nil")
	}
	if !strings.Contains(err.Error(), "/empresa") {
		t.Errorf("expected /empresa in error, got: %v", err)
	}
}

func TestValidate_ReportsAllBrokenLinksAtOnce(t *testing.T) {
	d := t.TempDir()
	writeHTML(t, d, "index.html", "/missing-a", "/missing-b", "/present")
	writeHTML(t, d, "present.html")

	err := linkcheck.ValidateAndReport(d, "", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "/missing-a") || !strings.Contains(err.Error(), "/missing-b") {
		t.Errorf("expected both broken links in error, got: %v", err)
	}
}

func TestValidate_EmptyDistIsNotAnError(t *testing.T) {
	d := t.TempDir()
	if err := linkcheck.ValidateAndReport(d, "", nil); err != nil {
		t.Fatalf("empty dist should not error, got: %v", err)
	}
}

// ── Skipped link types ────────────────────────────────────────────────────────

func TestValidate_SkipsExternalLinks(t *testing.T) {
	d := t.TempDir()
	writeHTML(t, d, "index.html",
		"https://external.com/page",
		"http://other.com",
		"//cdn.example.com/lib.js",
	)
	if err := linkcheck.ValidateAndReport(d, "", nil); err != nil {
		t.Fatalf("expected external links to be skipped, got: %v", err)
	}
}

func TestValidate_SkipsAnchors(t *testing.T) {
	d := t.TempDir()
	writeHTML(t, d, "index.html", "#section", "#top")
	if err := linkcheck.ValidateAndReport(d, "", nil); err != nil {
		t.Fatalf("expected anchor links to be skipped, got: %v", err)
	}
}

func TestValidate_SkipsMailtoAndTel(t *testing.T) {
	d := t.TempDir()
	writeHTML(t, d, "index.html", "mailto:hello@example.com", "tel:+1234567890")
	if err := linkcheck.ValidateAndReport(d, "", nil); err != nil {
		t.Fatalf("expected mailto/tel to be skipped, got: %v", err)
	}
}

func TestValidate_SkipsAnchorFragmentOnExistingPage(t *testing.T) {
	d := t.TempDir()
	writeHTML(t, d, "index.html", "/about#section")
	writeHTML(t, d, "about.html")
	if err := linkcheck.ValidateAndReport(d, "", nil); err != nil {
		t.Fatalf("expected /about#section to pass (page exists), got: %v", err)
	}
}

func TestValidate_SkipsQueryStringOnExistingPage(t *testing.T) {
	d := t.TempDir()
	writeHTML(t, d, "index.html", "/about?lang=en")
	writeHTML(t, d, "about.html")
	if err := linkcheck.ValidateAndReport(d, "", nil); err != nil {
		t.Fatalf("expected /about?lang=en to pass (page exists), got: %v", err)
	}
}

// ── Sibling (cross-deployment) links ─────────────────────────────────────────

func TestValidate_SkipsDeclaredSiblingLinks(t *testing.T) {
	// PT build (basePath=""): nav has links to /en/agenda and /jp/agenda.
	// Those pages live in the EN/JP deployments. Declare siblings to skip them.
	d := t.TempDir()
	writeHTML(t, d, "index.html", "/agenda", "/en/agenda", "/en/contato", "/jp/agenda")
	writeHTML(t, d, "agenda.html")

	// Siblings: /en and /jp are separate deployments sharing the same bucket.
	if err := linkcheck.ValidateAndReport(d, "", []string{"/en", "/jp"}); err != nil {
		t.Fatalf("expected sibling links to be skipped, got: %v", err)
	}
}

func TestValidate_CatchesBrokenLinkWhenNoSiblingsDeclared(t *testing.T) {
	// Without declaring siblings, /en/agenda is treated as a missing local page.
	d := t.TempDir()
	writeHTML(t, d, "index.html", "/agenda", "/en/agenda")
	writeHTML(t, d, "agenda.html")

	err := linkcheck.ValidateAndReport(d, "", nil) // no siblings declared
	if err == nil {
		t.Fatal("expected error: /en/agenda not in dist and not declared as sibling")
	}
	if !strings.Contains(err.Error(), "/en/agenda") {
		t.Errorf("expected /en/agenda in error, got: %v", err)
	}
}

func TestValidate_SiblingPrefixNormalisedWithoutLeadingSlash(t *testing.T) {
	// Siblings may be passed without a leading slash — normalised internally.
	d := t.TempDir()
	writeHTML(t, d, "index.html", "/agenda", "/en/agenda")
	writeHTML(t, d, "agenda.html")

	if err := linkcheck.ValidateAndReport(d, "", []string{"en"}); err != nil { // "en" not "/en"
		t.Fatalf("expected sibling 'en' to match /en/agenda, got: %v", err)
	}
}

func TestValidate_CatchesBrokenSubpathOfOwnPage(t *testing.T) {
	// /agenda exists but /agenda/sub-path doesn't — that's a broken link.
	d := t.TempDir()
	writeHTML(t, d, "index.html", "/agenda/sub-path")
	writeHTML(t, d, "agenda.html")

	err := linkcheck.ValidateAndReport(d, "", nil)
	if err == nil {
		t.Fatal("expected error for /agenda/sub-path (file not generated)")
	}
}

// ── basePath (EN deployment) ──────────────────────────────────────────────────

func TestValidate_ENDeployment_ValidatesOwnLinks(t *testing.T) {
	// EN build (basePath="/en"): all generated HTML has href="/en/page".
	// EN dist/ has the pages at root (no /en/ subdir).
	d := t.TempDir()
	writeHTML(t, d, "index.html", "/en/agenda", "/en/contato")
	writeHTML(t, d, "agenda.html")
	writeHTML(t, d, "contato.html")

	if err := linkcheck.ValidateAndReport(d, "/en", nil); err != nil {
		t.Fatalf("expected EN deployment links to pass, got: %v", err)
	}
}

func TestValidate_ENDeployment_CatchesBrokenLink(t *testing.T) {
	d := t.TempDir()
	writeHTML(t, d, "index.html", "/en/agenda", "/en/missing-page")
	writeHTML(t, d, "agenda.html")
	// missing-page.html intentionally absent

	err := linkcheck.ValidateAndReport(d, "/en", nil)
	if err == nil {
		t.Fatal("expected error for broken /en/missing-page")
	}
	if !strings.Contains(err.Error(), "/missing-page") {
		t.Errorf("expected /missing-page (stripped of /en) in error, got: %v", err)
	}
}

func TestValidate_ENDeployment_SkipsLinksToPTRoot(t *testing.T) {
	// EN HTML links back to the PT root ("/") — that's outside /en/ so it's
	// treated as cross-deployment and skipped.
	d := t.TempDir()
	writeHTML(t, d, "index.html", "/en/agenda", "/")
	writeHTML(t, d, "agenda.html")

	if err := linkcheck.ValidateAndReport(d, "/en", nil); err != nil {
		t.Fatalf("expected / to be skipped as outside /en namespace, got: %v", err)
	}
}

// ── Clean URLs ────────────────────────────────────────────────────────────────

func TestValidate_CleanURLs_IndexInSubdirReachable(t *testing.T) {
	d := t.TempDir()
	writeHTML(t, d, "index.html", "/about")
	writeHTML(t, d, "about/index.html") // clean URL: /about → about/index.html
	if err := linkcheck.ValidateAndReport(d, "", nil); err != nil {
		t.Fatalf("expected clean URL /about to resolve via about/index.html, got: %v", err)
	}
}

// ── Relative hrefs ────────────────────────────────────────────────────────────

func TestValidate_RelativeHrefResolvedFromRoot(t *testing.T) {
	// "about" in index.html at / → /about → about.html.
	d := t.TempDir()
	writeHTML(t, d, "index.html", "about")
	writeHTML(t, d, "about.html")
	if err := linkcheck.ValidateAndReport(d, "", nil); err != nil {
		t.Fatalf("expected relative href to resolve, got: %v", err)
	}
}

func TestValidate_RelativeHrefBrokenIsDetected(t *testing.T) {
	d := t.TempDir()
	writeHTML(t, d, "index.html", "ghost-page")

	err := linkcheck.ValidateAndReport(d, "", nil)
	if err == nil {
		t.Fatal("expected error for relative broken link ghost-page")
	}
}

// ── Internal-page exclusion (internal: true) ──────────────────────────────────

func TestValidate_LinksToInternalPageAreCaughtIfNotInDist(t *testing.T) {
	// agenda-editor is marked internal: true → NOT written to dist/.
	// If the nav still has a link to it, that's a bug the checker must catch.
	d := t.TempDir()
	writeHTML(t, d, "index.html", "/agenda", "/agenda-editor")
	writeHTML(t, d, "agenda.html")
	// agenda-editor.html intentionally absent (internal page)

	err := linkcheck.ValidateAndReport(d, "", nil)
	if err == nil {
		t.Fatal("expected broken link to internal page to be caught")
	}
	if !strings.Contains(err.Error(), "/agenda-editor") {
		t.Errorf("expected /agenda-editor in error, got: %v", err)
	}
}

// ── <base href> support ───────────────────────────────────────────────────────

// writeHTMLWithBase creates an HTML file with a <base href> tag and the given anchor hrefs.
func writeHTMLWithBase(t *testing.T, dir, relPath, baseHref string, hrefs ...string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	var sb strings.Builder
	sb.WriteString(`<!doctype html><html><head><base href="` + baseHref + `"></head><body>`)
	for _, h := range hrefs {
		sb.WriteString(`<a href="` + h + `">link</a>`)
	}
	sb.WriteString("</body></html>")
	if err := os.WriteFile(full, []byte(sb.String()), 0o644); err != nil { //nolint:gosec
		t.Fatalf("write %s: %v", relPath, err)
	}
}

func TestValidate_BaseHref_RelativeLinkResolvedFromBase(t *testing.T) {
	// about/index.html has <base href="/about">.
	// href="other" → resolves to /other (parent dir of /about is /).
	d := t.TempDir()
	writeHTMLWithBase(t, d, "about/index.html", "/about", "other")
	writeHTML(t, d, "index.html")
	writeHTML(t, d, "other.html")

	if err := linkcheck.ValidateAndReport(d, "", nil); err != nil {
		t.Fatalf("expected relative href under base href to resolve, got: %v", err)
	}
}

func TestValidate_BaseHref_CrossDeploymentLinkSkipped(t *testing.T) {
	// PT clean-URL page: about/index.html has <base href="/about">.
	// href="pt/about.html" → resolves to /pt/about.html → sibling "/pt" → skipped.
	d := t.TempDir()
	writeHTMLWithBase(t, d, "about/index.html", "/about", "pt/about.html")
	writeHTML(t, d, "index.html")

	if err := linkcheck.ValidateAndReport(d, "", []string{"/pt"}); err != nil {
		t.Fatalf("expected cross-deployment relative link to be skipped via sibling, got: %v", err)
	}
}

func TestValidate_BaseHref_TrailingSlashTreatedAsDirectory(t *testing.T) {
	// <base href="/about/"> — trailing slash means directory; href="sub" → /about/sub.
	d := t.TempDir()
	writeHTMLWithBase(t, d, "about/index.html", "/about/", "sub")
	writeHTML(t, d, "index.html")
	writeHTML(t, d, "about/sub.html")

	if err := linkcheck.ValidateAndReport(d, "", nil); err != nil {
		t.Fatalf("expected base href with trailing slash to resolve, got: %v", err)
	}
}

func TestValidate_BaseHref_ExternalBaseHrefFallsBackToFileDir(t *testing.T) {
	// External <base href="https://example.com/"> — ignored; fallback to file dir.
	d := t.TempDir()
	writeHTMLWithBase(t, d, "about/index.html", "https://example.com/", "/other")
	writeHTML(t, d, "index.html")
	writeHTML(t, d, "other.html")

	if err := linkcheck.ValidateAndReport(d, "", nil); err != nil {
		t.Fatalf("expected external base href to be ignored, got: %v", err)
	}
}

func TestValidate_BaseHref_BrokenLinkStillDetected(t *testing.T) {
	// Even with <base href>, a link to a page that does not exist must be caught.
	d := t.TempDir()
	writeHTMLWithBase(t, d, "about/index.html", "/about", "ghost")
	writeHTML(t, d, "index.html")
	// ghost.html intentionally absent

	err := linkcheck.ValidateAndReport(d, "", nil)
	if err == nil {
		t.Fatal("expected error for broken link resolved via base href, got nil")
	}
	if !strings.Contains(err.Error(), "/ghost") {
		t.Errorf("expected /ghost in error, got: %v", err)
	}
}

// ── Error formatting ──────────────────────────────────────────────────────────

func TestValidate_ErrorGroupedBySourceFile(t *testing.T) {
	d := t.TempDir()
	writeHTML(t, d, "index.html", "/missing-from-index")
	writeHTML(t, d, "about.html", "/missing-from-about")

	err := linkcheck.ValidateAndReport(d, "", nil)
	if err == nil {
		t.Fatal("expected errors")
	}
	if !strings.Contains(err.Error(), "index.html") || !strings.Contains(err.Error(), "about.html") {
		t.Errorf("expected both source files in error, got: %v", err)
	}
	if strings.Count(err.Error(), "broken internal link") != 1 {
		t.Errorf("expected single summary line, got: %v", err)
	}
}
