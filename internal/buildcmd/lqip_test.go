package buildcmd

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── test image helpers ────────────────────────────────────────────────────────

func makePNGBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x * 10), G: uint8(y * 10), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding test PNG: %v", err)
	}
	return buf.Bytes()
}

func makeJPEGBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatalf("encoding test JPEG: %v", err)
	}
	return buf.Bytes()
}

func writePNG(t *testing.T, dir, relPath string, w, h int) string {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, makePNGBytes(t, w, h), 0o644); err != nil {
		t.Fatalf("write PNG: %v", err)
	}
	return full
}

// ── generateLQIP ──────────────────────────────────────────────────────────────

func TestGenerateLQIP_ProducesJPEGDataURI(t *testing.T) {
	data := makePNGBytes(t, 200, 100)
	uri, err := generateLQIP(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(uri, "data:image/jpeg;base64,") {
		t.Fatalf("expected JPEG data URI, got prefix: %q", clampLen(uri, 50))
	}
}

func TestGenerateLQIP_ProducesSmallOutput(t *testing.T) {
	// A 800×600 PNG should produce a small LQIP (< 1 KB).
	data := makePNGBytes(t, 800, 600)
	uri, err := generateLQIP(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// base64 of a 20px wide quality-20 JPEG should be comfortably under 1 KB
	if len(uri) > 1024 {
		t.Errorf("expected LQIP data URI < 1024 bytes, got %d", len(uri))
	}
}

func TestGenerateLQIP_AcceptsJPEG(t *testing.T) {
	data := makeJPEGBytes(t, 100, 100)
	uri, err := generateLQIP(data)
	if err != nil {
		t.Fatalf("unexpected error for JPEG: %v", err)
	}
	if !strings.HasPrefix(uri, "data:image/jpeg;base64,") {
		t.Fatalf("expected JPEG data URI for JPEG input, got: %q", clampLen(uri, 50))
	}
}

func TestGenerateLQIP_FailsOnGarbage(t *testing.T) {
	_, err := generateLQIP([]byte("not an image"))
	if err == nil {
		t.Fatal("expected error for non-image data, got nil")
	}
}

func TestGenerateLQIP_FailsOnEmptyInput(t *testing.T) {
	_, err := generateLQIP([]byte{})
	if err == nil {
		t.Fatal("expected error for empty input, got nil")
	}
}

// ── buildLQIPTag ──────────────────────────────────────────────────────────────

func TestBuildLQIPTag_ReplacesSrcWithDataURIAndAddsDataSrc(t *testing.T) {
	tag := `<img src="/hero.png" alt="hero" loading="eager">`
	got := buildLQIPTag(tag, "/hero.png", "data:image/jpeg;base64,blur")
	if !strings.Contains(got, `src="data:image/jpeg;base64,blur"`) {
		t.Errorf("expected data URI in src, got: %s", got)
	}
	if !strings.Contains(got, `data-src="/hero.png"`) {
		t.Errorf("expected original src moved to data-src, got: %s", got)
	}
}

func TestBuildLQIPTag_AddsLQIPPendingClassWhenNone(t *testing.T) {
	tag := `<img src="/hero.png" loading="eager">`
	got := buildLQIPTag(tag, "/hero.png", "data:image/jpeg;base64,x")
	if !strings.Contains(got, `class="lqip-pending"`) {
		t.Errorf("expected lqip-pending class added, got: %s", got)
	}
}

func TestBuildLQIPTag_AppendsToExistingClass(t *testing.T) {
	tag := `<img src="/hero.png" class="hero-img" loading="eager">`
	got := buildLQIPTag(tag, "/hero.png", "data:image/jpeg;base64,x")
	if !strings.Contains(got, "hero-img") {
		t.Errorf("expected existing class preserved, got: %s", got)
	}
	if !strings.Contains(got, "lqip-pending") {
		t.Errorf("expected lqip-pending appended to class, got: %s", got)
	}
}

func TestBuildLQIPTag_SingleQuoteAttributes(t *testing.T) {
	tag := `<img src='/hero.png' loading='eager'>`
	got := buildLQIPTag(tag, "/hero.png", "data:image/jpeg;base64,x")
	if !strings.Contains(got, `data-src='/hero.png'`) {
		t.Errorf("expected single-quote attr handling, got: %s", got)
	}
}

// ── processLQIPImages ─────────────────────────────────────────────────────────

func TestProcessLQIPImages_ProcessesEagerPNGImage(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, dir, "images/hero.png", 100, 50)
	html := `<html><head></head><body><img src="/images/hero.png" loading="eager" alt="hero"></body></html>`
	got, err := processLQIPImages(html, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "data:image/jpeg;base64,") {
		t.Error("expected LQIP data URI in src")
	}
	if !strings.Contains(got, `data-src="/images/hero.png"`) {
		t.Error("expected original src in data-src")
	}
	if !strings.Contains(got, "lqip-pending") {
		t.Error("expected lqip-pending class")
	}
}

func TestProcessLQIPImages_InjectsLQIPCSSAndScriptOnce(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, dir, "a.png", 10, 10)
	writePNG(t, dir, "b.png", 10, 10)
	html := `<html><head></head><body>` +
		`<img src="/a.png" loading="eager">` +
		`<img src="/b.png" loading="eager">` +
		`</body></html>`
	got, err := processLQIPImages(html, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// CSS must appear exactly once.
	if strings.Count(got, "lqip-pending{filter:blur") != 1 {
		t.Errorf("expected LQIP CSS injected exactly once, got: %s", got)
	}
	// Script must appear exactly once.
	if strings.Count(got, "querySelectorAll") != 1 {
		t.Errorf("expected LQIP script injected exactly once, got: %s", got)
	}
}

func TestProcessLQIPImages_SkipsLazyLoadedImages(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, dir, "lazy.png", 10, 10)
	html := `<html><head></head><body><img src="/lazy.png" loading="lazy"></body></html>`
	got, err := processLQIPImages(html, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, "data:image/jpeg") {
		t.Error("expected lazy image not processed by LQIP")
	}
	if strings.Contains(got, "lqip-pending") {
		t.Error("expected no LQIP class for lazy image")
	}
}

func TestProcessLQIPImages_SkipsImagesWithoutLoadingAttr(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, dir, "normal.png", 10, 10)
	html := `<html><head></head><body><img src="/normal.png" alt="x"></body></html>`
	got, err := processLQIPImages(html, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, "lqip-pending") {
		t.Error("expected image without loading attr not processed")
	}
}

func TestProcessLQIPImages_SkipsSVGSources(t *testing.T) {
	dir := t.TempDir()
	html := `<html><head></head><body><img src="/logo.svg" loading="eager"></body></html>`
	got, err := processLQIPImages(html, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, "lqip-pending") {
		t.Error("expected SVG src skipped by LQIP")
	}
}

func TestProcessLQIPImages_SkipsExternalURLs(t *testing.T) {
	dir := t.TempDir()
	html := `<html><head></head><body><img src="https://cdn.example.com/hero.jpg" loading="eager"></body></html>`
	got, err := processLQIPImages(html, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, "lqip-pending") {
		t.Error("expected external URL skipped by LQIP")
	}
}

func TestProcessLQIPImages_SkipsAlreadyDataURI(t *testing.T) {
	dir := t.TempDir()
	html := `<html><head></head><body><img src="data:image/png;base64,abc" loading="eager"></body></html>`
	got, err := processLQIPImages(html, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, "lqip-pending") {
		t.Error("expected data URI src skipped by LQIP")
	}
}

func TestProcessLQIPImages_NoInjectionWhenNoEagerImages(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, dir, "img.png", 10, 10)
	html := `<html><head></head><body><img src="/img.png"></body></html>`
	got, err := processLQIPImages(html, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, "lqip-pending{filter:blur") {
		t.Error("expected no LQIP CSS when no eager images")
	}
}

func TestProcessLQIPImages_InjectsScriptBeforeBodyClose(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, dir, "hero.png", 10, 10)
	html := `<html><head></head><body><img src="/hero.png" loading="eager"></body></html>`
	got, err := processLQIPImages(html, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	bodyIdx := strings.Index(got, "</body>")
	scriptIdx := strings.Index(got, "querySelectorAll")
	if scriptIdx >= bodyIdx {
		t.Error("expected LQIP script injected before </body>")
	}
}

func TestProcessLQIPImages_FallsBackToBeforeHtmlClose(t *testing.T) {
	// Document without </body> — script must go before </html>.
	dir := t.TempDir()
	writePNG(t, dir, "hero.png", 10, 10)
	html := `<html><head></head><img src="/hero.png" loading="eager"></html>`
	got, err := processLQIPImages(html, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "querySelectorAll") {
		t.Error("expected LQIP script injected")
	}
}

func TestProcessLQIPImages_MissingFileSkipped(t *testing.T) {
	dir := t.TempDir()
	html := `<html><head></head><body><img src="/ghost.png" loading="eager"></body></html>`
	got, err := processLQIPImages(html, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Missing file: tag left unchanged, no LQIP injected.
	if strings.Contains(got, "lqip-pending") {
		t.Error("expected missing image skipped cleanly")
	}
}

// ── scaleNearest ──────────────────────────────────────────────────────────────

func TestScaleNearest_OutputDimensions(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 200, 100))
	dst := scaleNearest(src, 20, 10)
	b := dst.Bounds()
	if b.Dx() != 20 || b.Dy() != 10 {
		t.Errorf("expected 20×10, got %d×%d", b.Dx(), b.Dy())
	}
}

func TestScaleNearest_SinglePixelOutput(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 100, 100))
	dst := scaleNearest(src, 1, 1)
	b := dst.Bounds()
	if b.Dx() != 1 || b.Dy() != 1 {
		t.Errorf("expected 1×1, got %d×%d", b.Dx(), b.Dy())
	}
}

func TestScaleNearest_PreservesColorApproximately(t *testing.T) {
	// A solid red image should produce a red 1×1 pixel.
	src := image.NewRGBA(image.Rect(0, 0, 50, 50))
	for y := range 50 {
		for x := range 50 {
			src.Set(x, y, color.RGBA{R: 255, G: 0, B: 0, A: 255})
		}
	}
	dst := scaleNearest(src, 1, 1)
	r, g, b, _ := dst.At(0, 0).RGBA()
	if r>>8 < 200 || g>>8 > 10 || b>>8 > 10 {
		t.Errorf("expected red pixel, got r=%d g=%d b=%d", r>>8, g>>8, b>>8)
	}
}

func clampLen(s string, max int) string {
	if len(s) < max {
		return s
	}
	return s[:max]
}
