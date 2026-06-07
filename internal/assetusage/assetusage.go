package assetusage

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	stylesheetTagRE = regexp.MustCompile(`(?is)<link\s+[^>]*rel=["']stylesheet["'][^>]*href=["']([^"']+)["'][^>]*>`)
	scriptTagRE     = regexp.MustCompile(`(?is)<script\s+[^>]*src=["']([^"']+)["'][^>]*>\s*</script>`)
	cssImportRE     = regexp.MustCompile(`(?is)@import\s+(?:url\(\s*)?['"]?([^'"\)\s;]+)['"]?\s*\)?[^;]*;`)
	jsImportRE      = regexp.MustCompile(`(?m)^\s*import\s+(?:[^"'` + "`" + `]+?\s+from\s+)?["']([^"']+)["']`)
	jsExportFromRE  = regexp.MustCompile(`(?m)^\s*export\s+[^"'` + "`" + `]+?\s+from\s+["']([^"']+)["']`)
)

type Result struct {
	UsedCSS    []string
	UsedJS     []string
	UnusedCSS  []string
	UnusedJS   []string
	MissingRef []string
}

func Validate(assetsRoot string, renderedPages map[string]string) (Result, error) {
	allCSS, allJS, err := collectLocalAssets(assetsRoot)
	if err != nil {
		return Result{}, err
	}

	used, queue, err := collectInitialUsedAssets(renderedPages)
	if err != nil {
		return Result{}, err
	}

	missing, err := expandImportedAssets(assetsRoot, used, queue)
	if err != nil {
		return Result{}, err
	}

	result := buildResult(allCSS, allJS, used, missing)
	if err := validateResult(result); err != nil {
		return result, err
	}
	return result, nil
}

func collectInitialUsedAssets(renderedPages map[string]string) (used map[string]struct{}, queue []string, err error) {
	used = make(map[string]struct{})
	queue = make([]string, 0)

	for pageName, html := range renderedPages {
		for _, ref := range collectHTMLRefs(html) {
			relPath, local, err := normalizeHTMLAssetRef(ref)
			if err != nil {
				return nil, nil, fmt.Errorf("normalizing asset reference %q in %s: %w", ref, pageName, err)
			}
			if !local {
				continue
			}
			if !isCSSOrJS(relPath) {
				continue
			}
			if _, seen := used[relPath]; seen {
				continue
			}
			used[relPath] = struct{}{}
			queue = append(queue, relPath)
		}
	}

	return used, queue, nil
}

func isCSSOrJS(relPath string) bool {
	switch strings.ToLower(filepath.Ext(relPath)) {
	case ".css", ".js":
		return true
	default:
		return false
	}
}

func expandImportedAssets(assetsRoot string, used map[string]struct{}, queue []string) ([]string, error) {
	var missing []string

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		fullPath := filepath.Join(assetsRoot, filepath.FromSlash(current))
		imported, missingRef, err := importsForAsset(fullPath, current)
		if err != nil {
			return nil, err
		}
		if missingRef {
			missing = append(missing, current)
			continue
		}

		for _, relPath := range imported {
			if _, seen := used[relPath]; seen {
				continue
			}
			used[relPath] = struct{}{}
			queue = append(queue, relPath)
		}
	}

	return missing, nil
}

func importsForAsset(fullPath, relPath string) (imported []string, missing bool, err error) {
	info, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("stat asset %s: %w", fullPath, err)
	}
	if info.IsDir() {
		return nil, true, nil
	}

	switch strings.ToLower(filepath.Ext(relPath)) {
	case ".css":
		imported, err = collectCSSImports(fullPath, relPath)
	case ".js":
		imported, err = collectJSImports(fullPath, relPath)
	default:
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return imported, false, nil
}

func buildResult(allCSS, allJS map[string]struct{}, used map[string]struct{}, missing []string) Result {
	return Result{
		UsedCSS:    intersectAndSort(allCSS, used),
		UsedJS:     intersectAndSort(allJS, used),
		UnusedCSS:  differenceAndSort(allCSS, used),
		UnusedJS:   differenceAndSort(allJS, used),
		MissingRef: sortStrings(missing),
	}
}

func validateResult(result Result) error {
	errs := make([]string, 0, len(result.MissingRef)+len(result.UnusedCSS)+len(result.UnusedJS))
	for _, relPath := range result.MissingRef {
		errs = append(errs, fmt.Sprintf("missing local asset reference: %s", relPath))
	}
	for _, relPath := range result.UnusedCSS {
		errs = append(errs, fmt.Sprintf("unused local css asset: %s", relPath))
	}
	for _, relPath := range result.UnusedJS {
		errs = append(errs, fmt.Sprintf("unused local js asset: %s", relPath))
	}
	if len(errs) == 0 {
		return nil
	}
	return errors.New(strings.Join(errs, "; "))
}

func collectLocalAssets(assetsRoot string) (map[string]struct{}, map[string]struct{}, error) {
	allCSS := make(map[string]struct{})
	allJS := make(map[string]struct{})
	err := filepath.WalkDir(assetsRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".css" && ext != ".js" {
			return nil
		}
		relPath, err := filepath.Rel(assetsRoot, path)
		if err != nil {
			return err
		}
		normalized := filepath.ToSlash(relPath)
		if ext == ".css" {
			allCSS[normalized] = struct{}{}
		} else {
			allJS[normalized] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("walking assets root %s: %w", assetsRoot, err)
	}
	return allCSS, allJS, nil
}

func collectHTMLRefs(doc string) []string {
	var refs []string
	for _, match := range stylesheetTagRE.FindAllStringSubmatch(doc, -1) {
		if len(match) > 1 {
			refs = append(refs, match[1])
		}
	}
	for _, match := range scriptTagRE.FindAllStringSubmatch(doc, -1) {
		if len(match) > 1 {
			refs = append(refs, match[1])
		}
	}
	return refs
}

func collectCSSImports(fullPath, relPath string) ([]string, error) {
	raw, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, fmt.Errorf("reading css asset %s: %w", fullPath, err)
	}
	baseDir := pathDir(relPath)
	var refs []string
	for _, match := range cssImportRE.FindAllStringSubmatch(string(raw), -1) {
		if len(match) < 2 {
			continue
		}
		next, ok, err := normalizeNestedAssetRef(baseDir, match[1], ".css")
		if err != nil {
			return nil, fmt.Errorf("normalizing css import %q in %s: %w", match[1], relPath, err)
		}
		if ok {
			refs = append(refs, next)
		}
	}
	return refs, nil
}

func collectJSImports(fullPath, relPath string) ([]string, error) {
	raw, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, fmt.Errorf("reading js asset %s: %w", fullPath, err)
	}
	baseDir := pathDir(relPath)
	var refs []string
	for _, pattern := range []*regexp.Regexp{jsImportRE, jsExportFromRE} {
		for _, match := range pattern.FindAllStringSubmatch(string(raw), -1) {
			if len(match) < 2 {
				continue
			}
			next, ok, err := normalizeNestedAssetRef(baseDir, match[1], ".js")
			if err != nil {
				return nil, fmt.Errorf("normalizing js import %q in %s: %w", match[1], relPath, err)
			}
			if ok {
				refs = append(refs, next)
			}
		}
	}
	return refs, nil
}

func normalizeHTMLAssetRef(ref string) (string, bool, error) {
	return normalizeRef("", ref, "")
}

func normalizeNestedAssetRef(baseDir, ref, expectedExt string) (string, bool, error) {
	return normalizeRef(baseDir, ref, expectedExt)
}

func normalizeRef(baseDir, ref, expectedExt string) (string, bool, error) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return "", false, nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", false, err
	}
	if shouldSkipAssetURL(parsed, trimmed) {
		return "", false, nil
	}

	joined := joinAndCleanAssetPath(baseDir, parsed.Path)
	if isInvalidAssetPath(joined) {
		return "", false, nil
	}

	joined = ensureExpectedExt(joined, expectedExt)
	if !isCSSOrJS(joined) {
		return "", false, nil
	}

	return joined, true, nil
}

func shouldSkipAssetURL(parsed *url.URL, trimmedRef string) bool {
	if parsed.Scheme != "" || parsed.Host != "" {
		return true
	}
	if strings.HasPrefix(trimmedRef, "//") {
		return true
	}

	cleanPath := strings.TrimSpace(parsed.Path)
	if cleanPath == "" || strings.HasPrefix(cleanPath, "#") {
		return true
	}

	lower := strings.ToLower(cleanPath)
	return strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "javascript:") || strings.HasPrefix(lower, "vbscript:")
}

func joinAndCleanAssetPath(baseDir, rawPath string) string {
	cleanPath := strings.TrimSpace(rawPath)
	if strings.HasPrefix(cleanPath, "/") {
		return pathClean(strings.TrimPrefix(cleanPath, "/"))
	}
	if baseDir != "" {
		return pathClean(baseDir + "/" + cleanPath)
	}
	return pathClean(cleanPath)
}

func isInvalidAssetPath(joined string) bool {
	return joined == "." || joined == "" || strings.HasPrefix(joined, "../")
}

func ensureExpectedExt(joined, expectedExt string) string {
	if expectedExt == "" {
		return joined
	}
	if filepath.Ext(joined) != "" {
		return joined
	}
	return joined + expectedExt
}

func pathClean(v string) string {
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(v)))
	return strings.TrimPrefix(cleaned, "./")
}

func pathDir(v string) string {
	dir := filepath.ToSlash(filepath.Dir(filepath.FromSlash(v)))
	if dir == "." {
		return ""
	}
	return dir
}

func intersectAndSort(all map[string]struct{}, used map[string]struct{}) []string {
	values := make([]string, 0)
	for path := range all {
		if _, ok := used[path]; ok {
			values = append(values, path)
		}
	}
	sort.Strings(values)
	return values
}

func differenceAndSort(all map[string]struct{}, used map[string]struct{}) []string {
	values := make([]string, 0)
	for path := range all {
		if _, ok := used[path]; ok {
			continue
		}
		values = append(values, path)
	}
	sort.Strings(values)
	return values
}

func sortStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	clone := append([]string(nil), values...)
	sort.Strings(clone)
	return clone
}
