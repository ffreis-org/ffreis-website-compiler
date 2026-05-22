package buildcmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ffreis-website-compiler/internal/testutil"
)

// ── assetContentHash ──────────────────────────────────────────────────────────

func TestAssetContentHash_Is8HexChars(t *testing.T) {
	hash := assetContentHash([]byte("hello"))
	if len(hash) != 8 {
		t.Fatalf("expected 8-char hash, got %d: %q", len(hash), hash)
	}
	for _, c := range hash {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Fatalf("expected lowercase hex, got %q", hash)
		}
	}
}

func TestAssetContentHash_Deterministic(t *testing.T) {
	data := []byte("some css content")
	h1 := assetContentHash(data)
	h2 := assetContentHash(data)
	if h1 != h2 {
		t.Fatal("expected same input to produce same hash")
	}
}

func TestAssetContentHash_DifferentContentDifferentHash(t *testing.T) {
	if assetContentHash([]byte("aaa")) == assetContentHash([]byte("bbb")) {
		t.Fatal("expected different content to produce different hashes")
	}
}

func TestAssetContentHash_EmptyInput(t *testing.T) {
	// Must not panic and must produce a valid 8-char hash.
	hash := assetContentHash([]byte{})
	if len(hash) != 8 {
		t.Fatalf("expected 8 chars for empty input, got %q", hash)
	}
}

// ── insertHashInPath ──────────────────────────────────────────────────────────

func TestInsertHashInPath_SingleExtension(t *testing.T) {
	got := insertHashInPath("portrait.webp", "a1b2c3d4")
	if got != "portrait.a1b2c3d4.webp" {
		t.Fatalf("got %q", got)
	}
}

func TestInsertHashInPath_PreservesDirectory(t *testing.T) {
	got := insertHashInPath("fonts/inter.woff2", "deadbeef")
	if got != "fonts/inter.deadbeef.woff2" {
		t.Fatalf("got %q", got)
	}
}

func TestInsertHashInPath_DoubleExtension(t *testing.T) {
	// Only the last extension is preserved; e.g. "file.min.js"
	got := insertHashInPath("file.min.js", "12345678")
	if got != "file.min.12345678.js" {
		t.Fatalf("got %q", got)
	}
}

func TestInsertHashInPath_NoExtension(t *testing.T) {
	// No extension → hash appended with no trailing dot
	got := insertHashInPath("favicon", "abcd1234")
	if got != "favicon.abcd1234" {
		t.Fatalf("got %q", got)
	}
}

// ── isDataURI ─────────────────────────────────────────────────────────────────

func TestIsDataURI_TrueForDataPrefix(t *testing.T) {
	cases := []string{
		"data:image/png;base64,abc",
		"data:font/woff2;base64,xyz",
		"DATA:text/css,body{}",
		"  data:image/jpeg;base64,", // leading space
	}
	for _, c := range cases {
		if !isDataURI(c) {
			t.Errorf("expected isDataURI=true for %q", c)
		}
	}
}

func TestIsDataURI_FalseForNonData(t *testing.T) {
	cases := []string{"", "/fonts/x.woff2", "https://example.com", "./rel.png"}
	for _, c := range cases {
		if isDataURI(c) {
			t.Errorf("expected isDataURI=false for %q", c)
		}
	}
}

// ── isExternalRef ─────────────────────────────────────────────────────────────

func TestIsExternalRef(t *testing.T) {
	external := []string{
		"http://example.com/x.css",
		"https://cdn.example.com/lib.js",
		"//example.com/font.woff2",
		"HTTP://UPPER.CASE/",
		"  https://leading-space.com/",
	}
	for _, r := range external {
		if !isExternalRef(r) {
			t.Errorf("expected isExternalRef=true for %q", r)
		}
	}

	local := []string{
		"",
		"/css/main.css",
		"css/main.css",
		"./relative.css",
		"../up.css",
		"data:text/css,body{}",
	}
	for _, r := range local {
		if isExternalRef(r) {
			t.Errorf("expected isExternalRef=false for %q", r)
		}
	}
}

// ── isSVGPath ─────────────────────────────────────────────────────────────────

func TestIsSVGPath(t *testing.T) {
	yes := []string{"/images/logo.svg", "icon.SVG", "/a/b/c.svg", "relative.svg"}
	for _, p := range yes {
		if !isSVGPath(p) {
			t.Errorf("expected isSVGPath=true for %q", p)
		}
	}
	no := []string{"/images/photo.png", "script.js", "data:image/svg+xml,", ""}
	for _, p := range no {
		if isSVGPath(p) {
			t.Errorf("expected isSVGPath=false for %q", p)
		}
	}
}

// ── isFontRef ─────────────────────────────────────────────────────────────────

func TestIsFontRef(t *testing.T) {
	yes := []string{
		"fonts/inter.woff2", "/fonts/roboto.woff", "font.ttf",
		"path/regular.otf", "old.eot", "FONT.WOFF2",
	}
	for _, r := range yes {
		if !isFontRef(r) {
			t.Errorf("expected isFontRef=true for %q", r)
		}
	}
	no := []string{"/css/main.css", "script.js", "image.png", "", "font.txt"}
	for _, r := range no {
		if isFontRef(r) {
			t.Errorf("expected isFontRef=false for %q", r)
		}
	}
}

// ── fingerprintLocalAssets ────────────────────────────────────────────────────

func newFingerprintDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		testutil.MustMkdirAll(t, filepath.Dir(p))
		testutil.WriteFiles(t, map[string]string{p: content})
	}
	return dir
}

func TestFingerprintLocalAssets_FingerprintsImgSrc(t *testing.T) {
	dir := newFingerprintDir(t, map[string]string{
		"images/logo.png": "pngdata",
	})
	html := `<html><body><img src="/images/logo.png" alt="logo"></body></html>`
	got, toCopy, err := fingerprintLocalAssets(html, dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, `src="/images/logo.png"`) {
		t.Error("expected original src replaced with hashed version")
	}
	if !strings.Contains(got, "logo.") {
		t.Error("expected fingerprinted filename in output")
	}
	if len(toCopy) == 0 {
		t.Error("expected toCopy to contain the hashed asset")
	}
	// The hash must be consistent with assetContentHash of the file content.
	want := assetContentHash([]byte("pngdata"))
	if !strings.Contains(got, want) {
		t.Errorf("expected hash %q in output, got: %s", want, got)
	}
}

func TestFingerprintLocalAssets_SameAssetTwiceHashedOnce(t *testing.T) {
	dir := newFingerprintDir(t, map[string]string{
		"images/logo.png": "pngdata",
	})
	html := `<html><body>
		<img src="/images/logo.png" alt="a">
		<img src="/images/logo.png" alt="b">
	</body></html>`
	got, toCopy, err := fingerprintLocalAssets(html, dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(toCopy) != 1 {
		t.Errorf("expected 1 entry in toCopy for deduplicated asset, got %d", len(toCopy))
	}
	// Both img tags should use the same hashed path.
	hash := assetContentHash([]byte("pngdata"))
	if strings.Count(got, hash) != 2 {
		t.Errorf("expected hash %q to appear twice (one per img), got: %s", hash, got)
	}
}

func TestFingerprintLocalAssets_SkipsExternalRefs(t *testing.T) {
	dir := t.TempDir()
	html := `<html><body><img src="https://cdn.example.com/logo.png"></body></html>`
	got, toCopy, err := fingerprintLocalAssets(html, dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "cdn.example.com") {
		t.Error("expected external URL left unchanged")
	}
	if len(toCopy) != 0 {
		t.Errorf("expected empty toCopy for external-only page, got %v", toCopy)
	}
}

func TestFingerprintLocalAssets_SkipsDataURIs(t *testing.T) {
	dir := t.TempDir()
	html := `<html><body><img src="data:image/png;base64,abc=" alt="x"></body></html>`
	got, _, err := fingerprintLocalAssets(html, dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "data:image/png;base64,abc=") {
		t.Error("expected data URI src left unchanged")
	}
}

func TestFingerprintLocalAssets_FingerprintsDataSrc(t *testing.T) {
	// data-src is written by LQIP; the full-res image must also be fingerprinted.
	dir := newFingerprintDir(t, map[string]string{"images/photo.webp": "webpdata"})
	html := `<img class="lqip-pending" src="data:image/jpeg;base64,blurry" data-src="/images/photo.webp">`
	got, toCopy, err := fingerprintLocalAssets(html, dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, `data-src="/images/photo.webp"`) {
		t.Error("expected data-src fingerprinted")
	}
	hash := assetContentHash([]byte("webpdata"))
	if !strings.Contains(got, hash) {
		t.Errorf("expected hash %q in data-src, got: %s", hash, got)
	}
	if len(toCopy) != 1 {
		t.Errorf("expected 1 toCopy entry, got %d", len(toCopy))
	}
}

func TestFingerprintLocalAssets_FingerprintsScriptSrc(t *testing.T) {
	dir := newFingerprintDir(t, map[string]string{"js/analytics.js": "console.log(1);"})
	html := `<html><head></head><body><script src="/js/analytics.js"></script></body></html>`
	got, toCopy, err := fingerprintLocalAssets(html, dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, `src="/js/analytics.js"`) {
		t.Error("expected script src fingerprinted")
	}
	if len(toCopy) != 1 {
		t.Errorf("expected 1 toCopy entry, got %d", len(toCopy))
	}
}

func TestFingerprintLocalAssets_FingerprintsIconHref(t *testing.T) {
	dir := newFingerprintDir(t, map[string]string{"favicon.ico": "ico"})
	html := `<html><head><link rel="icon" href="/favicon.ico"></head></html>`
	got, toCopy, err := fingerprintLocalAssets(html, dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, `href="/favicon.ico"`) {
		t.Error("expected icon href fingerprinted")
	}
	if len(toCopy) != 1 {
		t.Errorf("expected 1 toCopy entry, got %d", len(toCopy))
	}
}

func TestFingerprintLocalAssets_FingerprintsManifestHref(t *testing.T) {
	dir := newFingerprintDir(t, map[string]string{"manifest.json": `{"name":"app"}`})
	html := `<html><head><link rel="manifest" href="/manifest.json"></head></html>`
	got, _, err := fingerprintLocalAssets(html, dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, `href="/manifest.json"`) {
		t.Error("expected manifest href fingerprinted")
	}
}

func TestFingerprintLocalAssets_FingerprintsPreloadHref(t *testing.T) {
	dir := newFingerprintDir(t, map[string]string{"fonts/inter.woff2": "woff2data"})
	html := `<link rel="preload" href="/fonts/inter.woff2" as="font" crossorigin>`
	got, toCopy, err := fingerprintLocalAssets(html, dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, `href="/fonts/inter.woff2"`) {
		t.Error("expected preload href fingerprinted")
	}
	if len(toCopy) != 1 {
		t.Errorf("expected 1 toCopy entry, got %d", len(toCopy))
	}
}

func TestFingerprintLocalAssets_FingerprintsCSSURLInStyleBlock(t *testing.T) {
	dir := newFingerprintDir(t, map[string]string{"fonts/inter.woff2": "fontbytes"})
	html := `<html><head><style>@font-face{src:url("/fonts/inter.woff2")}</style></head></html>`
	got, toCopy, err := fingerprintLocalAssets(html, dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, `"/fonts/inter.woff2"`) {
		t.Error("expected url() inside <style> fingerprinted")
	}
	if len(toCopy) != 1 {
		t.Errorf("expected 1 toCopy entry, got %d", len(toCopy))
	}
	hash := assetContentHash([]byte("fontbytes"))
	if !strings.Contains(got, hash) {
		t.Errorf("expected hash %q in style block, got: %s", hash, got)
	}
}

func TestFingerprintLocalAssets_MissingAssetLeftAsIs(t *testing.T) {
	dir := t.TempDir()
	html := `<img src="/images/ghost.png">`
	got, toCopy, err := fingerprintLocalAssets(html, dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, `src="/images/ghost.png"`) {
		t.Error("expected missing asset left unchanged")
	}
	if len(toCopy) != 0 {
		t.Errorf("expected empty toCopy for missing asset, got %v", toCopy)
	}
}

func TestFingerprintLocalAssets_ToCopyMapsHashedToOriginal(t *testing.T) {
	dir := newFingerprintDir(t, map[string]string{"css/main.css": "body{}"})
	// Reference via a <style>-less path — use script src to exercise toCopy.
	html := `<script src="/css/main.css"></script>`
	_, toCopy, err := fingerprintLocalAssets(html, dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(toCopy) != 1 {
		t.Fatalf("expected 1 toCopy entry, got %d", len(toCopy))
	}
	for hashed, original := range toCopy {
		if original != "css/main.css" {
			t.Errorf("expected original=css/main.css, got %q", original)
		}
		hash := assetContentHash([]byte("body{}"))
		if !strings.Contains(hashed, hash) {
			t.Errorf("expected hash %q in hashed path %q", hash, hashed)
		}
	}
}

func TestFingerprintLocalAssets_RootRelativePreservesLeadingSlash(t *testing.T) {
	dir := newFingerprintDir(t, map[string]string{"images/logo.png": "p"})
	html := `<img src="/images/logo.png">`
	got, _, err := fingerprintLocalAssets(html, dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Fingerprinted path must still start with /.
	if !strings.Contains(got, `src="/images/logo.`) {
		t.Errorf("expected root-relative fingerprinted path, got: %s", got)
	}
}

func TestFingerprintLocalAssets_BasePathPrependedToAbsoluteRefs(t *testing.T) {
	// Deployments served under a path prefix (e.g. petlook.app/en) must have
	// their root-absolute asset refs rewritten to include the prefix, otherwise
	// the browser requests them at the wrong path and they 404 (silently for
	// fonts, which then fall back to a system font).
	dir := newFingerprintDir(t, map[string]string{"fonts/inter.woff2": "fontbytes"})
	html := `<html><head><style>@font-face{src:url("/fonts/inter.woff2")}</style></head>` +
		`<body><img src="/fonts/inter.woff2"></body></html>`
	got, toCopy, err := fingerprintLocalAssets(html, dir, "/en")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	hash := assetContentHash([]byte("fontbytes"))
	// Both the <style> url() and the <img src> must be prefixed with /en/.
	wantSubstr := "/en/fonts/inter." + hash + ".woff2"
	if !strings.Contains(got, wantSubstr) {
		t.Errorf("expected %q in output, got: %s", wantSubstr, got)
	}
	// The toCopy map keys are the on-disk paths (unprefixed) so the packer
	// copies fonts/inter.HASH.woff2 — base_path is a URL concept, not a fs path.
	if len(toCopy) != 1 {
		t.Fatalf("expected 1 toCopy entry, got %d: %v", len(toCopy), toCopy)
	}
	for hashed := range toCopy {
		if strings.HasPrefix(hashed, "en/") || strings.HasPrefix(hashed, "/en/") {
			t.Errorf("toCopy key must not include base_path; got %q", hashed)
		}
	}
}

func TestFingerprintLocalAssets_BasePathWithTrailingSlashNormalized(t *testing.T) {
	// Defensive: site_data["base_path"] is "/en" by convention but the
	// rewriter must tolerate a trailing slash without double-slashing the URL.
	dir := newFingerprintDir(t, map[string]string{"images/logo.png": "p"})
	html := `<img src="/images/logo.png">`
	got, _, err := fingerprintLocalAssets(html, dir, "/en/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, "/en//images/") {
		t.Errorf("expected trailing slash on base_path to be normalized, got: %s", got)
	}
	if !strings.Contains(got, `src="/en/images/logo.`) {
		t.Errorf("expected /en/images/logo.<hash>.png, got: %s", got)
	}
}

func TestFingerprintLocalAssets_EmptyBasePathBehavesAsRoot(t *testing.T) {
	// Empty base_path means root deployment; absolute refs stay /X (no prefix).
	dir := newFingerprintDir(t, map[string]string{"images/logo.png": "p"})
	html := `<img src="/images/logo.png">`
	got, _, err := fingerprintLocalAssets(html, dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, `src="/images/logo.`) {
		t.Errorf("expected root-absolute fingerprinted path, got: %s", got)
	}
	// Must not have any double-slash artifact.
	if strings.Contains(got, "//images/") {
		t.Errorf("empty base_path must not produce //, got: %s", got)
	}
}

// ── readAsset security ────────────────────────────────────────────────────────

func TestReadAsset_PathTraversalRejected(t *testing.T) {
	dir := t.TempDir()
	// Create a file outside the asset root.
	secretPath := filepath.Join(filepath.Dir(dir), "secret.txt")
	if err := os.WriteFile(secretPath, []byte("secret"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, _, err := readAsset(dir, "../secret.txt")
	if err == nil {
		t.Fatal("expected error for path traversal, got nil")
	}
}

func TestReadAsset_AbsoluteLeadingSlashNormalized(t *testing.T) {
	dir := newFingerprintDir(t, map[string]string{"css/main.css": "body{}"})
	data, cleanRef, err := readAsset(dir, "/css/main.css")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != "body{}" {
		t.Errorf("expected file content, got %q", data)
	}
	if cleanRef != "css/main.css" {
		t.Errorf("expected clean ref without leading slash, got %q", cleanRef)
	}
}

// ── detectMimeType ────────────────────────────────────────────────────────────

func TestDetectMimeType(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"file.css", "text/css"},
		{"file.js", "application/javascript"},
		{"file.svg", "image/svg+xml"},
		{"file.png", "image/png"},
		{"file.jpg", "image/jpeg"},
		{"file.jpeg", "image/jpeg"},
		{"file.woff", "font/woff"},
		{"file.woff2", "font/woff2"},
		{"file.ttf", "font/ttf"},
		{"file.ico", "image/x-icon"},
	}
	for _, tc := range cases {
		got := detectMimeType(tc.path, nil)
		if got != tc.want {
			t.Errorf("detectMimeType(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestDetectMimeType_UnknownExtensionSniffs(t *testing.T) {
	// Unknown extension falls back to http.DetectContentType.
	got := detectMimeType("file.bin", []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}) // PNG magic bytes
	if !strings.HasPrefix(got, "image/") {
		t.Errorf("expected image/ MIME for PNG bytes, got %q", got)
	}
}

// ── writeHashedAssets ─────────────────────────────────────────────────────────

func TestWriteHashedAssets_WritesCorrectContent(t *testing.T) {
	srcDir := newFingerprintDir(t, map[string]string{"css/main.css": "body{color:red}"})
	dstDir := t.TempDir()
	hash := assetContentHash([]byte("body{color:red}"))
	hashedRel := fmt.Sprintf("css/main.%s.css", hash)
	toCopy := map[string]string{hashedRel: "css/main.css"}
	if err := writeHashedAssets(dstDir, srcDir, toCopy); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	written, err := os.ReadFile(filepath.Join(dstDir, filepath.FromSlash(hashedRel)))
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}
	if string(written) != "body{color:red}" {
		t.Errorf("expected original content in hashed file, got %q", written)
	}
}
