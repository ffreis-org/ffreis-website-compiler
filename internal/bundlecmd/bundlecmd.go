// Package bundlecmd emits the petlook mobile content bundle: a per-language JSON projection of the
// merged, validated petlook-data, consumed by the native iOS + Android apps (petlook-mobile). It
// reuses sitegen.LoadSiteData for the shared+lang layer merge so the bundle and the website build
// share one data-loading path. See petlook-mobile/contract/ for the schema the output conforms to.
package bundlecmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"ffreis-website-compiler/internal/sitegen"

	"gopkg.in/yaml.v3"
)

// Whitelisted keys projected into the bundle. Keys absent from data are dropped unless listed as
// required (then emit fails — the drift signal). Mirrors petlook-mobile/contract/content-bundle.schema.json.
var (
	navKeys       = []string{"skip_link", "logo_aria", "nav_aria", "recommendations", "contact", "donate", "donate_url", "lang_links"}
	navRequired   = []string{"recommendations", "contact", "donate", "donate_url", "lang_links"}
	generatorKeys = []string{
		"headline", "subtext", "url_label", "url_placeholder", "url_hint", "photo_label",
		"photo_drop_label", "photo_hint", "photo_remove_label", "url_add", "url_remove_label",
		"cta", "generating_label", "loading_stages", "result_download", "result_retry", "result_alt",
		"convert_16bit_cta", "convert_16bit_loading", "convert_16bit_label", "convert_16bit_alt",
		"convert_16bit_download", "convert_16bit_retry", "convert_16bit_error",
		"result_products_heading", "disclosure_inline", "disclosure_more", "disclosure_title",
		"disclosure_close", "disclosure_body_intro", "disclosure_items",
	}
	generatorRequired  = []string{"headline", "subtext", "url_label", "url_placeholder", "photo_label", "cta", "generating_label", "result_download", "result_retry", "convert_16bit_cta", "result_products_heading", "disclosure_inline"}
	errorKeys          = []string{"url_required", "photo_required", "generic"}
	langLinkKeys       = []string{"lang", "label", "flag", "href", "active"}
	recommendationKeys = []string{"id", "store", "name", "url", "image_url", "price"}
	brandKeys          = []string{"brand_id", "brand_name", "brand_url", "logo_url", "description", "placement_type", "disclosure_label", "priority"}
)

// Options holds parsed CLI flags.
type Options struct {
	DataRoot      string
	WebsiteRoot   string
	Langs         []string
	Out           string
	SchemaVersion int
	CDNBase       string
	SourceSHA     string
	GeneratedAt   string
}

// Run is the entry point for the emit-content-bundle subcommand.
func Run(args []string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	opts, err := parseOptions(args)
	if err != nil {
		return err
	}

	langs := opts.Langs
	if len(langs) == 0 {
		if langs, err = detectLangs(opts.DataRoot); err != nil {
			return fmt.Errorf("detecting languages: %w", err)
		}
	}
	if len(langs) == 0 {
		return fmt.Errorf("no language directories found under %s", opts.DataRoot)
	}

	bundles := make(map[string]map[string]any, len(langs))
	for _, lang := range langs {
		b, err := buildBundle(opts, lang)
		if err != nil {
			return fmt.Errorf("lang %q: %w", lang, err)
		}
		bundles[lang] = b
	}

	if err := checkParity(bundles); err != nil {
		return err
	}

	if err := writeBundles(opts, langs, bundles); err != nil {
		return err
	}
	logger.Info("emitted content bundle", "langs", strings.Join(langs, ","), "out", opts.Out)
	return nil
}

func parseOptions(args []string) (Options, error) {
	fs := flag.NewFlagSet("emit-content-bundle", flag.ContinueOnError)
	var opts Options
	var langs string
	fs.StringVar(&opts.DataRoot, "data-root", "", "path to the petlook-data data/ directory (required)")
	fs.StringVar(&opts.WebsiteRoot, "website-root", "", "optional path to petlook-website (reserved for future contract validation)")
	fs.StringVar(&langs, "langs", "", "comma-separated language codes; auto-detected if empty")
	fs.StringVar(&opts.Out, "out", "dist/content-bundle", "output directory")
	fs.IntVar(&opts.SchemaVersion, "schema-version", 1, "bundle schema version")
	fs.StringVar(&opts.CDNBase, "cdn-base", "https://petlook.app", "CDN base for rewriting brand logo_s3_uri")
	fs.StringVar(&opts.SourceSHA, "source-sha", "0000000", "petlook-data commit SHA recorded in the bundle")
	fs.StringVar(&opts.GeneratedAt, "generated-at", "1970-01-01T00:00:00Z", "RFC3339 timestamp recorded in the bundle (pass a fixed value for reproducibility)")
	if err := fs.Parse(args); err != nil {
		return Options{}, err
	}
	if opts.DataRoot == "" {
		return Options{}, fmt.Errorf("-data-root is required")
	}
	opts.Langs = splitCSV(langs)
	return opts, nil
}

// detectLangs returns non-"shared" immediate subdirectories of dataRoot, sorted.
func detectLangs(dataRoot string) ([]string, error) {
	entries, err := os.ReadDir(dataRoot)
	if err != nil {
		return nil, err
	}
	var langs []string
	for _, e := range entries {
		if e.IsDir() && e.Name() != "shared" {
			langs = append(langs, e.Name())
		}
	}
	sort.Strings(langs)
	return langs, nil
}

// buildBundle merges shared+lang layers and projects the whitelisted bundle for one language.
func buildBundle(opts Options, lang string) (map[string]any, error) {
	sharedDir := filepath.Join(opts.DataRoot, "shared", "site.d")
	langDir := filepath.Join(opts.DataRoot, lang, "site.d")
	// sitegen merges layers in order; the lang layer overrides shared.
	source := sharedDir + "|" + langDir
	res, err := sitegen.LoadSiteData("", source)
	if err != nil {
		return nil, fmt.Errorf("loading site data: %w", err)
	}
	data := res.Data

	ui := asMap(data["ui"])
	if len(ui) == 0 {
		return nil, fmt.Errorf("missing 'ui' block")
	}

	nav, err := projectMap(asMap(ui["nav"]), navKeys, navRequired, "ui.nav")
	if err != nil {
		return nil, err
	}
	if ll, ok := nav["lang_links"]; ok {
		nav["lang_links"] = projectLangLinks(ll)
	}
	gen, err := projectMap(asMap(ui["generator"]), generatorKeys, generatorRequired, "ui.generator")
	if err != nil {
		return nil, err
	}
	errs, err := projectMap(asMap(ui["errors"]), errorKeys, errorKeys, "ui.errors")
	if err != nil {
		return nil, err
	}

	bundle := map[string]any{
		"schema_version": opts.SchemaVersion,
		"lang":           lang,
		"html_lang":      htmlLang(opts.DataRoot, lang),
		"generated_at":   opts.GeneratedAt,
		"source_sha":     opts.SourceSHA,
		"ui": map[string]any{
			"nav":       nav,
			"generator": gen,
			"errors":    errs,
		},
		"recommendations": projectRecommendations(data["recommendations"]),
		"brands":          projectBrands(asMap(data["brands"]), opts.CDNBase),
	}
	bundle["content_hash"] = contentHash(bundle)
	return bundle, nil
}

// projectMap copies whitelisted keys that exist; errors if a required key is absent.
func projectMap(src map[string]any, keys, required []string, path string) (map[string]any, error) {
	out := map[string]any{}
	for _, k := range keys {
		if v, ok := src[k]; ok {
			out[k] = v
		}
	}
	for _, k := range required {
		if _, ok := out[k]; !ok {
			return nil, fmt.Errorf("required key %s.%s is missing from petlook-data", path, k)
		}
	}
	return out, nil
}

func projectLangLinks(v any) []any {
	items, _ := v.([]any)
	out := make([]any, 0, len(items))
	for _, it := range items {
		m := asMap(it)
		row := map[string]any{}
		for _, k := range langLinkKeys {
			if val, ok := m[k]; ok {
				row[k] = val
			}
		}
		out = append(out, row)
	}
	return out
}

func projectRecommendations(v any) []any {
	items, _ := v.([]any)
	out := make([]any, 0, len(items))
	for _, it := range items {
		m := asMap(it)
		row := map[string]any{}
		for _, k := range recommendationKeys {
			row[k] = stringOrEmpty(m[k])
		}
		out = append(out, row)
	}
	return out
}

// projectBrands keeps only active brands, sorts by priority asc, and rewrites logo_s3_uri → absolute CDN URL.
func projectBrands(src map[string]any, cdnBase string) map[string]any {
	out := map[string]any{
		"section_title": stringOrEmpty(src["section_title"]),
		"partner_cta":   stringOrEmpty(src["partner_cta"]),
	}
	rawItems, _ := src["items"].([]any)
	type kv struct {
		prio int
		row  map[string]any
	}
	var rows []kv
	for _, it := range rawItems {
		m := asMap(it)
		if active, ok := m["active"].(bool); ok && !active {
			continue
		}
		row := map[string]any{}
		for _, k := range brandKeys {
			if k == "logo_url" {
				continue
			}
			if v, ok := m[k]; ok {
				row[k] = v
			}
		}
		if s3, ok := m["logo_s3_uri"].(string); ok && s3 != "" {
			row["logo_url"] = cdnBase + "/assets/" + s3
		}
		rows = append(rows, kv{prio: intOrZero(m["priority"]), row: row})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].prio < rows[j].prio })
	items := make([]any, 0, len(rows))
	for _, r := range rows {
		items = append(items, r.row)
	}
	out["items"] = items
	return out
}

// checkParity ensures every language bundle has the identical set of flattened key paths.
func checkParity(bundles map[string]map[string]any) error {
	var ref string
	var refKeys map[string]struct{}
	langs := make([]string, 0, len(bundles))
	for l := range bundles {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	for _, lang := range langs {
		keys := flatten(bundles[lang], "")
		// Volatile metadata differs per lang by design.
		for _, k := range []string{"content_hash", "lang", "html_lang", "generated_at", "source_sha"} {
			delete(keys, k)
		}
		if refKeys == nil {
			ref, refKeys = lang, keys
			continue
		}
		if only := diff(refKeys, keys); len(only) > 0 {
			return fmt.Errorf("parity: keys in %q absent from %q: %v", ref, lang, only)
		}
		if only := diff(keys, refKeys); len(only) > 0 {
			return fmt.Errorf("parity: keys in %q absent from %q: %v", lang, ref, only)
		}
	}
	return nil
}

func writeBundles(opts Options, langs []string, bundles map[string]map[string]any) error {
	if err := os.MkdirAll(opts.Out, 0o755); err != nil {
		return err
	}
	index := map[string]any{
		"schema_version": opts.SchemaVersion,
		"generated_at":   opts.GeneratedAt,
	}
	langEntries := map[string]any{}
	for _, lang := range langs {
		name := fmt.Sprintf("content.%s.v%d.json", lang, opts.SchemaVersion)
		if err := writeJSON(filepath.Join(opts.Out, name), bundles[lang]); err != nil {
			return err
		}
		langEntries[lang] = map[string]any{"path": name, "content_hash": bundles[lang]["content_hash"]}
	}
	index["langs"] = langEntries
	return writeJSON(filepath.Join(opts.Out, "index.json"), index)
}

// writeJSON writes deterministic, indented JSON (map keys are sorted by encoding/json).
func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644) //nolint:gosec // scan-fix(gosec:G306): public web-asset JSON served over HTTP; 0644 is intentional, 0600 would break delivery
}

// contentHash is sha256 over the payload excluding the volatile metadata fields.
func contentHash(bundle map[string]any) string {
	clone := map[string]any{}
	for k, v := range bundle {
		switch k {
		case "content_hash", "generated_at", "source_sha":
			continue
		default:
			clone[k] = v
		}
	}
	data, _ := json.Marshal(clone)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// htmlLang reads <lang>/site.yaml for html_lang; falls back to a sensible default.
func htmlLang(dataRoot, lang string) string {
	var doc struct {
		HTMLLang string `yaml:"html_lang"`
	}
	if raw, err := os.ReadFile(filepath.Join(dataRoot, lang, "site.yaml")); err == nil {
		_ = yaml.Unmarshal(raw, &doc)
	}
	if doc.HTMLLang != "" {
		return doc.HTMLLang
	}
	switch lang {
	case "jp":
		return "ja"
	case "pt":
		return "pt-BR"
	default:
		return lang
	}
}

// ── small helpers ────────────────────────────────────────────────────────────

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func stringOrEmpty(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func intOrZero(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// flatten returns the set of dotted key paths (objects recursed; arrays/scalars are leaves).
func flatten(m map[string]any, prefix string) map[string]struct{} {
	out := map[string]struct{}{}
	for k, v := range m {
		full := k
		if prefix != "" {
			full = prefix + "." + k
		}
		out[full] = struct{}{}
		if child, ok := v.(map[string]any); ok {
			for ck := range flatten(child, full) {
				out[ck] = struct{}{}
			}
		}
	}
	return out
}

func diff(a, b map[string]struct{}) []string {
	var only []string
	for k := range a {
		if _, ok := b[k]; !ok {
			only = append(only, k)
		}
	}
	sort.Strings(only)
	return only
}
