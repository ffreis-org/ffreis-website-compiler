package bundlecmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stableArgs mirror the flags used to generate testdata/golden — keep in sync if regenerating.
func stableArgs(dataRoot, out string) []string {
	return []string{
		"-data-root", dataRoot,
		"-langs", "en,pt",
		"-out", out,
		"-schema-version", "1",
		"-source-sha", "testsha",
		"-generated-at", "2026-01-01T00:00:00Z",
	}
}

func TestEmit_MatchesGolden(t *testing.T) {
	out := t.TempDir()
	if err := Run(stableArgs("testdata/data", out), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, name := range []string{"content.en.v1.json", "content.pt.v1.json", "index.json"} {
		got, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatalf("read output %s: %v", name, err)
		}
		want, err := os.ReadFile(filepath.Join("testdata/golden", name))
		if err != nil {
			t.Fatalf("read golden %s: %v", name, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from golden (regenerate with `go run ./cmd/emit-content-bundle %s`)", name,
				strings.Join(stableArgs("internal/bundlecmd/testdata/data", "internal/bundlecmd/testdata/golden"), " "))
		}
	}
}

func TestEmit_Deterministic(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if err := Run(stableArgs("testdata/data", a), nil); err != nil {
		t.Fatal(err)
	}
	if err := Run(stableArgs("testdata/data", b), nil); err != nil {
		t.Fatal(err)
	}
	x, _ := os.ReadFile(filepath.Join(a, "content.en.v1.json"))
	y, _ := os.ReadFile(filepath.Join(b, "content.en.v1.json"))
	if string(x) != string(y) {
		t.Error("emit is not deterministic across runs")
	}
}

func TestEmit_MissingRequiredKey_Fails(t *testing.T) {
	root := writeTree(t, map[string]string{
		"shared/site.d/10-rec.yaml": "recommendations: []\n",
		"en/site.yaml":              "html_lang: en\n",
		// ui.errors.generic intentionally omitted
		"en/site.d/50-ui.yaml": minimalUI("en", true, "") + "  errors:\n    url_required: u\n    photo_required: p\n",
	})
	err := Run([]string{"-data-root", root, "-langs", "en", "-out", t.TempDir()}, nil)
	if err == nil || !strings.Contains(err.Error(), "ui.errors.generic") {
		t.Fatalf("expected missing-required error for ui.errors.generic, got: %v", err)
	}
}

func TestEmit_ParityDrift_Fails(t *testing.T) {
	// en has disclosure_more; pt does not → parity must fail.
	root := writeTree(t, map[string]string{
		"shared/site.d/10-rec.yaml": "recommendations: []\n",
		"en/site.yaml":              "html_lang: en\n",
		"pt/site.yaml":              "html_lang: pt-BR\n",
		// en's generator has an extra whitelisted key (disclosure_more) that pt lacks.
		"en/site.d/50-ui.yaml": minimalUI("en", true, "    disclosure_more: more\n") + fullErrors(),
		"pt/site.d/50-ui.yaml": minimalUI("pt", false, "") + fullErrors(),
	})
	err := Run([]string{"-data-root", root, "-langs", "en,pt", "-out", t.TempDir()}, nil)
	if err == nil || !strings.Contains(err.Error(), "parity") {
		t.Fatalf("expected parity error, got: %v", err)
	}
}

func TestProjectBrands_FiltersInactiveSortsAndRewritesLogo(t *testing.T) {
	src := map[string]any{
		"section_title": "S",
		"partner_cta":   "P",
		"items": []any{
			map[string]any{"brand_id": "hi", "priority": 99, "active": true, "logo_s3_uri": "brands/hi.png"},
			map[string]any{"brand_id": "lo", "priority": 1, "active": true, "logo_s3_uri": "brands/lo.png"},
			map[string]any{"brand_id": "off", "priority": 0, "active": false, "logo_s3_uri": "brands/off.png"},
		},
	}
	out := projectBrands(src, "https://cdn.test")
	items := out["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("expected 2 active items, got %d", len(items))
	}
	first := items[0].(map[string]any)
	if first["brand_id"] != "lo" {
		t.Errorf("expected lowest priority first, got %v", first["brand_id"])
	}
	if first["logo_url"] != "https://cdn.test/assets/brands/lo.png" {
		t.Errorf("logo rewrite wrong: %v", first["logo_url"])
	}
	if _, leaked := first["active"]; leaked {
		t.Error("active flag should not be in output")
	}
	if _, leaked := first["logo_s3_uri"]; leaked {
		t.Error("logo_s3_uri should not be in output")
	}
}

func TestContentHash_ChangesWithPayload(t *testing.T) {
	base := map[string]any{"schema_version": 1, "lang": "en", "ui": map[string]any{"x": "1"}}
	h1 := contentHash(base)
	base["ui"].(map[string]any)["x"] = "2"
	if contentHash(base) == h1 {
		t.Error("content_hash must change when payload changes")
	}
	// volatile fields must NOT affect the hash
	base["ui"].(map[string]any)["x"] = "1"
	base["generated_at"] = "2030-01-01T00:00:00Z"
	base["source_sha"] = "deadbeef"
	if contentHash(base) != h1 {
		t.Error("content_hash must ignore generated_at/source_sha")
	}
}

// ── test helpers ─────────────────────────────────────────────────────────────

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "data")
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// minimalUI returns a ui block with nav + all required generator keys (no errors block).
// extraGen is appended inside the generator block (already indented).
func minimalUI(lang string, active bool, extraGen string) string {
	a := "false"
	if active {
		a = "true"
	}
	return "ui:\n" +
		"  nav:\n" +
		"    recommendations: r\n    contact: c\n    donate: d\n    donate_url: https://x\n" +
		"    lang_links:\n      - lang: " + lang + "\n        label: L\n        active: " + a + "\n" +
		"  generator:\n" +
		"    headline: h\n    subtext: s\n    url_label: u\n    url_placeholder: p\n    photo_label: ph\n" +
		"    cta: c\n    generating_label: g\n    result_download: rd\n    result_retry: rr\n" +
		"    convert_16bit_cta: x\n    result_products_heading: rp\n    disclosure_inline: di\n" +
		extraGen
}

func fullErrors() string {
	return "  errors:\n    url_required: u\n    photo_required: p\n    generic: g\n"
}
