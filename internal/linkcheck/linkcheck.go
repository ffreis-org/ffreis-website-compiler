// Package linkcheck validates that every internal <a href> link in a compiled
// static site points to a page that was actually generated in the output
// directory. It catches broken navigation links — the most common cause of
// "access denied" (HTTP 403) pages in production — before the site is promoted
// to the live S3 bucket.
//
// What it checks:
//   - Every <a href="/page"> in the compiled HTML must have a corresponding
//     file in outDir (either page.html or page/index.html for clean URLs).
//
// What it skips (not broken, not validated here):
//   - External links (http://, https://, //)
//   - Anchor-only links (#section)
//   - Non-navigational schemes (mailto:, tel:, javascript:)
//   - Cross-deployment links: e.g. a PT site linking to /en/page — these are
//     validated when the EN deployment runs its own link check. Sibling prefixes
//     must be declared via the siblingBasePaths parameter to avoid false positives.
package linkcheck

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// anchorHrefRE extracts href values from <a> tags only (not <link>, <img>, etc.)
// so asset references validated by assetusage are not double-reported here.
var anchorHrefRE = regexp.MustCompile(`(?is)<a\s+[^>]*href=["']([^"']+)["'][^>]*>`)

// baseHrefRE extracts the href from a <base href="..."> tag if present.
// Pages that use <base href="/path"> change the resolution base for all
// relative URLs in that page; the link checker must honour this to avoid
// false positives on cross-deployment language-switcher links.
var baseHrefRE = regexp.MustCompile(`(?is)<base\s+[^>]*href=["']([^"']+)["'][^>]*>`)

// skippedSchemes are URL prefixes that are never local page links.
var skippedSchemes = []string{
	"http://", "https://", "//",
	"mailto:", "tel:", "javascript:", "data:",
}

// Result holds the outcome of a link check run.
type Result struct {
	BrokenLinks []BrokenLink
}

// BrokenLink records a single broken internal link.
type BrokenLink struct {
	SourceFile string // dist-relative path of the HTML file containing the link
	Href       string // the raw href value as it appears in the HTML
	Target     string // resolved URL path that was not found
}

// Validate walks all HTML files in outDir, extracts every <a href> link, and
// verifies that any link targeting this deployment's own page namespace resolves
// to a file that exists in outDir. Returns an error listing all broken links.
//
// basePath is the site's URL prefix: "" for the root deployment, "/en" for an
// EN deployment mounted at /en/, etc.
//
// siblingBasePaths lists the URL prefixes of sibling deployments that share the
// same CloudFront distribution (e.g. ["/en", "/jp"] for the PT build of a
// three-language site). Links whose paths fall under a sibling prefix are
// skipped because they are validated by that sibling's own build. Pass nil or
// empty for single-language sites.
func Validate(outDir, basePath string, siblingBasePaths []string) (Result, error) {
	basePath = strings.TrimRight(basePath, "/")
	siblings := normaliseSiblings(siblingBasePaths)

	reachable, err := collectReachable(outDir)
	if err != nil {
		return Result{}, fmt.Errorf("scanning dist output: %w", err)
	}

	var result Result
	walkErr := filepath.WalkDir(outDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".html") {
			return err
		}
		broken, fileErr := checkFileLinks(path, outDir, basePath, siblings, reachable)
		if fileErr != nil {
			return fileErr
		}
		result.BrokenLinks = append(result.BrokenLinks, broken...)
		return nil
	})
	if walkErr != nil {
		return Result{}, walkErr
	}

	sort.Slice(result.BrokenLinks, func(i, j int) bool {
		if result.BrokenLinks[i].SourceFile != result.BrokenLinks[j].SourceFile {
			return result.BrokenLinks[i].SourceFile < result.BrokenLinks[j].SourceFile
		}
		return result.BrokenLinks[i].Href < result.BrokenLinks[j].Href
	})
	return result, nil
}

// ValidateAndReport calls Validate and formats the results as a human-readable
// error string listing every broken link by source file. Returns nil if all
// internal links are valid.
func ValidateAndReport(outDir, basePath string, siblingBasePaths []string) error {
	result, err := Validate(outDir, basePath, siblingBasePaths)
	if err != nil {
		return err
	}
	if len(result.BrokenLinks) == 0 {
		return nil
	}
	return fmt.Errorf("%s", formatBrokenLinks(result.BrokenLinks))
}

// ── internal helpers ──────────────────────────────────────────────────────────

// checkFileLinks reads an HTML file and returns BrokenLink entries for every
// internal anchor href that does not resolve to an existing dist file.
func checkFileLinks(path, outDir, basePath string, siblings []string, reachable map[string]bool) ([]BrokenLink, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	rel, _ := filepath.Rel(outDir, path)
	rel = filepath.ToSlash(rel)

	fileDir := "/" + filepath.ToSlash(filepath.Dir(rel))
	if fileDir == "/." {
		fileDir = "/"
	}

	// If the page declares a <base href>, relative URLs resolve against that
	// base rather than the file's own directory. This matters for clean-URL
	// pages (e.g. about/index.html with <base href="/about">) where templates
	// emit relative cross-deployment links like href="pt/about.html".
	resolutionBase := fileDir
	if base := extractBaseHref(string(content)); base != "" {
		if dir := baseHrefDir(base); dir != "" {
			resolutionBase = dir
		}
	}

	var broken []BrokenLink
	for _, href := range extractAnchorHrefs(string(content)) {
		target := resolveLink(href, resolutionBase, basePath, siblings)
		if target != "" && !reachable[target] {
			broken = append(broken, BrokenLink{SourceFile: rel, Href: href, Target: target})
		}
	}
	return broken, nil
}

// extractBaseHref returns the href value from the first <base> tag in doc,
// or "" if none is present.
func extractBaseHref(doc string) string {
	m := baseHrefRE.FindStringSubmatch(doc)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// baseHrefDir returns the effective directory for relative URL resolution
// given a <base href> value. Only absolute-path base hrefs are supported;
// external base hrefs (http://, https://) return "" so the caller falls
// back to the file's own directory.
//
//   - "/about"   → "/"   (parent of the path, no trailing slash)
//   - "/about/"  → "/about" (trailing slash means it IS a directory)
//   - "/en/page" → "/en"
func baseHrefDir(baseHref string) string {
	if !strings.HasPrefix(baseHref, "/") {
		return "" // external base href — ignore
	}
	if strings.HasSuffix(baseHref, "/") {
		dir := strings.TrimSuffix(baseHref, "/")
		if dir == "" {
			return "/"
		}
		return dir
	}
	idx := strings.LastIndexByte(baseHref, '/')
	if idx <= 0 {
		return "/"
	}
	return baseHref[:idx]
}

// normaliseSiblings cleans sibling base-path strings: ensures each starts with
// "/" and has no trailing slash.
func normaliseSiblings(raw []string) []string {
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		s = strings.TrimRight(s, "/")
		if s == "" {
			continue
		}
		if !strings.HasPrefix(s, "/") {
			s = "/" + s
		}
		out = append(out, s)
	}
	return out
}

// collectReachable walks outDir and builds the set of URL paths that correspond
// to generated HTML files. Both the bare-path form (/page) and the .html form
// (/page.html) are included so that links with or without .html extension are
// handled. For index files, the directory path is also recorded (/dir/).
func collectReachable(outDir string) (map[string]bool, error) {
	paths := make(map[string]bool)
	err := filepath.WalkDir(outDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".html") {
			return err
		}
		rel := filepath.ToSlash(strings.TrimPrefix(filepath.ToSlash(path), filepath.ToSlash(outDir)+"/"))
		addReachablePaths(paths, rel)
		return nil
	})
	return paths, err
}

// addReachablePaths records all URL forms for a dist-relative HTML file path.
// For index.html files (clean URLs), the directory forms (/dir, /dir/) are
// recorded along with the literal file path (/dir/index.html) so that relative
// self-referencing hrefs like href="index.html" do not produce false positives.
func addReachablePaths(paths map[string]bool, rel string) {
	if filepath.Base(rel) == "index.html" {
		dir := filepath.Dir(rel)
		if dir == "." {
			paths["/"] = true
			paths["/index.html"] = true
		} else {
			dir = filepath.ToSlash(dir)
			paths["/"+dir] = true
			paths["/"+dir+"/"] = true
			paths["/"+dir+"/index.html"] = true
		}
	} else {
		bare := strings.TrimSuffix(rel, ".html")
		paths["/"+bare] = true
		paths["/"+bare+".html"] = true
	}
}

// extractAnchorHrefs returns the href attribute values from all <a> tags in doc.
func extractAnchorHrefs(doc string) []string {
	matches := anchorHrefRE.FindAllStringSubmatch(doc, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if href := strings.TrimSpace(m[1]); href != "" {
			out = append(out, href)
		}
	}
	return out
}

// resolveLink converts a raw href into the URL path to look up in reachable.
// Returns "" when the link should be skipped (external, anchor, or sibling-deployment).
func resolveLink(href, fileDir, basePath string, siblings []string) string {
	href = stripFragmentAndQuery(href)
	if href == "" || isSkippedScheme(href) {
		return ""
	}
	href = makeAbsolute(href, fileDir)
	href = cleanURLPath(href)
	href = stripBasePath(href, basePath)
	if href == skipSignal {
		return ""
	}
	if isSiblingLink(href, siblings) {
		return ""
	}
	return href
}

// skipSignal is returned by stripBasePath when the link belongs to a different
// deployment root and should be skipped entirely.
const skipSignal = "\x00skip"

func stripFragmentAndQuery(href string) string {
	if idx := strings.IndexByte(href, '#'); idx != -1 {
		href = href[:idx]
	}
	if idx := strings.IndexByte(href, '?'); idx != -1 {
		href = href[:idx]
	}
	return href
}

func isSkippedScheme(href string) bool {
	lower := strings.ToLower(href)
	for _, prefix := range skippedSchemes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func makeAbsolute(href, fileDir string) string {
	if strings.HasPrefix(href, "/") {
		return href
	}
	if fileDir == "/" {
		return "/" + href
	}
	return fileDir + "/" + href
}

func stripBasePath(href, basePath string) string {
	if basePath == "" {
		if href == "" {
			return "/"
		}
		return href
	}
	switch {
	case href == basePath:
		return "/"
	case strings.HasPrefix(href, basePath+"/"):
		return href[len(basePath):]
	default:
		return skipSignal // outside this deployment's namespace
	}
}

func isSiblingLink(href string, siblings []string) bool {
	for _, s := range siblings {
		if href == s || strings.HasPrefix(href, s+"/") {
			return true
		}
	}
	return false
}

// cleanURLPath removes redundant slashes and resolves . and .. segments so
// that relative hrefs like ../sibling or ./page are correctly evaluated.
func cleanURLPath(p string) string {
	// Collapse consecutive slashes first.
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	// Resolve . and .. segments.
	var out []string
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "", ".":
			// skip empty and current-dir segments (leading "/" kept via empty first element)
		case "..":
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
		default:
			out = append(out, seg)
		}
	}
	result := "/" + strings.Join(out, "/")
	// Preserve trailing slash if the original had one.
	if strings.HasSuffix(p, "/") && !strings.HasSuffix(result, "/") {
		result += "/"
	}
	return result
}

// formatBrokenLinks returns a human-readable summary of broken links grouped by
// source file.
func formatBrokenLinks(broken []BrokenLink) string {
	byFile := make(map[string][]BrokenLink)
	for _, bl := range broken {
		byFile[bl.SourceFile] = append(byFile[bl.SourceFile], bl)
	}
	files := make([]string, 0, len(byFile))
	for f := range byFile {
		files = append(files, f)
	}
	sort.Strings(files)

	var sb strings.Builder
	fmt.Fprintf(&sb, "%d broken internal link(s) in compiled output:\n", len(broken))
	for _, file := range files {
		sb.WriteString("  " + file + ":\n")
		for _, bl := range byFile[file] {
			fmt.Fprintf(&sb, "    href=%q → %q not found\n", bl.Href, bl.Target)
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}
