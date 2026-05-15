package buildcmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ffreis-website-compiler/internal/testutil"
)

// minimalPNG is a 1×1 white PNG (67 bytes) for testing image inlining.
var minimalPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
	0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41,
	0x54, 0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00,
	0x00, 0x00, 0x02, 0x00, 0x01, 0xe2, 0x21, 0xbc,
	0x33, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e,
	0x44, 0xae, 0x42, 0x60, 0x82,
}

func mustWriteBytes(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestInlineSmallLocalRasterImages_InlinesBelowThreshold(t *testing.T) {
	dir := t.TempDir()
	mustWriteBytes(t, filepath.Join(dir, "images", "icon.png"), minimalPNG)
	doc := `<html><body><img src="/images/icon.png" alt="icon"></body></html>`
	got, err := inlineSmallLocalRasterImages(doc, dir, 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, `src="/images/icon.png"`) {
		t.Fatalf("expected original src removed, got %q", got)
	}
	if !strings.Contains(got, "data:image/png;base64,") {
		t.Fatalf("expected data URI in img src, got %q", got)
	}
}

func TestInlineSmallLocalRasterImages_KeepsAtThreshold(t *testing.T) {
	dir := t.TempDir()
	mustWriteBytes(t, filepath.Join(dir, "images", "big.png"), minimalPNG)
	doc := `<html><body><img src="/images/big.png" alt="x"></body></html>`
	// threshold equals file size → must NOT inline (len >= threshold means skip)
	got, err := inlineSmallLocalRasterImages(doc, dir, len(minimalPNG))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, `src="/images/big.png"`) {
		t.Fatalf("expected large image kept external, got %q", got)
	}
}

func TestInlineSmallLocalRasterImages_SkipsExternalURLs(t *testing.T) {
	dir := t.TempDir()
	doc := `<html><body><img src="https://cdn.example.com/logo.png" alt="x"></body></html>`
	got, err := inlineSmallLocalRasterImages(doc, dir, 1024*1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "cdn.example.com") {
		t.Fatalf("expected external img untouched, got %q", got)
	}
}

func TestInlineSmallLocalRasterImages_SkipsDataURIs(t *testing.T) {
	dir := t.TempDir()
	// Simulate an LQIP-processed image: src is already a data URI.
	doc := `<html><body><img class="lqip-pending" src="data:image/jpeg;base64,abc=" data-src="/images/photo.webp"></body></html>`
	got, err := inlineSmallLocalRasterImages(doc, dir, 1024*1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, `src="data:image/jpeg;base64,abc="`) {
		t.Fatalf("expected LQIP data URI src preserved, got %q", got)
	}
}

func TestInlineSmallLocalRasterImages_SkipsSVGs(t *testing.T) {
	dir := t.TempDir()
	testutil.MustMkdirAll(t, filepath.Join(dir, "images"))
	testutil.WriteFiles(t, map[string]string{
		filepath.Join(dir, "images", "icon.svg"): `<svg xmlns="http://www.w3.org/2000/svg"><circle r="5"/></svg>`,
	})
	doc := `<html><body><img src="/images/icon.svg" alt="icon"></body></html>`
	got, err := inlineSmallLocalRasterImages(doc, dir, 1024*1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, `src="/images/icon.svg"`) {
		t.Fatalf("expected SVG kept for inlineLocalSVGs, got %q", got)
	}
}
