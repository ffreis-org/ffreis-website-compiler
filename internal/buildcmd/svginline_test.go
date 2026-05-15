package buildcmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	smallSVG = `<?xml version="1.0" encoding="UTF-8"?>
<svg id="icon" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24">
  <circle cx="12" cy="12" r="10"/>
</svg>`

	smallSVGNoXML = `<svg id="icon" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24">
  <circle cx="12" cy="12" r="10"/>
</svg>`
)

func writeSVGAsset(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write svg asset: %v", err)
	}
	return p
}

func TestInlineLocalSVGs_ReplacesSmallSVGImg(t *testing.T) {
	dir := t.TempDir()
	writeSVGAsset(t, dir, "images/icon.svg", smallSVG)

	doc := `<img src="images/icon.svg" class="my-icon" alt="Icon" width="24" height="24">`
	got, err := inlineLocalSVGs(doc, dir)
	if err != nil {
		t.Fatalf("inlineLocalSVGs: %v", err)
	}

	if strings.Contains(got, "<img") {
		t.Error("expected <img> to be replaced, but it is still present")
	}
	if !strings.Contains(got, "<svg") {
		t.Error("expected inline <svg> in output")
	}
	if strings.Contains(got, "<?xml") {
		t.Error("expected XML processing instruction to be stripped")
	}
	if !strings.Contains(got, `class="my-icon`) {
		t.Errorf("expected img class merged into <svg>, got: %s", got)
	}
	if !strings.Contains(got, `aria-label="Icon"`) {
		t.Errorf("expected aria-label from alt, got: %s", got)
	}
	if !strings.Contains(got, `role="img"`) {
		t.Errorf("expected role=img when alt is non-empty, got: %s", got)
	}
}

func TestInlineLocalSVGs_EmptyAltBecomesAriaHidden(t *testing.T) {
	dir := t.TempDir()
	writeSVGAsset(t, dir, "images/deco.svg", smallSVG)

	doc := `<img src="images/deco.svg" alt="">`
	got, err := inlineLocalSVGs(doc, dir)
	if err != nil {
		t.Fatalf("inlineLocalSVGs: %v", err)
	}

	if !strings.Contains(got, `aria-hidden="true"`) {
		t.Errorf("expected aria-hidden=true for decorative SVG, got: %s", got)
	}
	if strings.Contains(got, `role="img"`) {
		t.Errorf("expected no role=img for decorative SVG, got: %s", got)
	}
}

func TestInlineLocalSVGs_SkipsExternalRefs(t *testing.T) {
	dir := t.TempDir()
	doc := `<img src="https://example.com/icon.svg" alt="ext">`
	got, err := inlineLocalSVGs(doc, dir)
	if err != nil {
		t.Fatalf("inlineLocalSVGs: %v", err)
	}
	if got != doc {
		t.Errorf("expected external SVG to be unchanged, got: %s", got)
	}
}

func TestInlineLocalSVGs_SkipsNonSVG(t *testing.T) {
	dir := t.TempDir()
	writeSVGAsset(t, dir, "images/photo.jpg", "JFIF")

	doc := `<img src="images/photo.jpg" alt="photo">`
	got, err := inlineLocalSVGs(doc, dir)
	if err != nil {
		t.Fatalf("inlineLocalSVGs: %v", err)
	}
	if got != doc {
		t.Errorf("expected non-SVG img to be unchanged, got: %s", got)
	}
}

func TestInlineLocalSVGs_SkipsOversizeSVG(t *testing.T) {
	dir := t.TempDir()
	big := "<svg>" + strings.Repeat("<!-- padding -->", svgInlineSizeLimit/16+1) + "</svg>"
	writeSVGAsset(t, dir, "images/big.svg", big)

	doc := `<img src="images/big.svg" alt="big">`
	got, err := inlineLocalSVGs(doc, dir)
	if err != nil {
		t.Fatalf("inlineLocalSVGs: %v", err)
	}
	if got != doc {
		t.Errorf("expected oversize SVG img to be unchanged, got: %s", got)
	}
}

func TestInlineLocalSVGs_MissingFileLeftAsIs(t *testing.T) {
	dir := t.TempDir()
	doc := `<img src="images/missing.svg" alt="missing">`
	got, err := inlineLocalSVGs(doc, dir)
	if err != nil {
		t.Fatalf("inlineLocalSVGs: %v", err)
	}
	if got != doc {
		t.Errorf("expected missing SVG img to be unchanged, got: %s", got)
	}
}

func TestSVGFromImgTag_StripsXMLDeclaration(t *testing.T) {
	result := svgFromImgTag(`<img src="x.svg" alt="">`, []byte(smallSVG))
	if strings.Contains(result, "<?xml") {
		t.Errorf("expected XML declaration stripped, got: %s", result)
	}
	if !strings.Contains(result, "<svg") {
		t.Errorf("expected <svg> in result, got: %s", result)
	}
}

func TestSVGFromImgTag_MergesClassIntoExistingSVGClass(t *testing.T) {
	svgWithClass := `<svg class="base-class" viewBox="0 0 24 24"><circle/></svg>`
	result := svgFromImgTag(`<img src="x.svg" class="my-icon" alt="">`, []byte(svgWithClass))
	if !strings.Contains(result, `class="my-icon base-class"`) {
		t.Errorf("expected merged class, got: %s", result)
	}
}

func TestSVGFromImgTag_AddsWidthHeight(t *testing.T) {
	result := svgFromImgTag(`<img src="x.svg" alt="" width="48" height="48">`, []byte(smallSVGNoXML))
	if !strings.Contains(result, `width="48"`) {
		t.Errorf("expected width=48 added to svg, got: %s", result)
	}
	if !strings.Contains(result, `height="48"`) {
		t.Errorf("expected height=48 added to svg, got: %s", result)
	}
}

func TestSVGFromImgTag_DoesNotDuplicateWidthWhenSVGAlreadyHasIt(t *testing.T) {
	svgWithDims := `<svg width="100" height="100" viewBox="0 0 100 100"><rect/></svg>`
	result := svgFromImgTag(`<img src="x.svg" alt="" width="48" height="48">`, []byte(svgWithDims))
	// Should contain the original width, not the img's width
	if strings.Count(result, `width="`) > 1 {
		t.Errorf("expected only one width attribute, got: %s", result)
	}
}

func TestSVGFromImgTag_EscapesAltInAriaLabel(t *testing.T) {
	result := svgFromImgTag(`<img src="x.svg" alt='Brand &amp; Co'>`, []byte(smallSVGNoXML))
	if !strings.Contains(result, `aria-label="Brand &amp; Co"`) {
		t.Errorf("expected HTML-escaped aria-label, got: %s", result)
	}
}
