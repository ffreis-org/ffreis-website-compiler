package buildcmd

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// inlineLocalAssets inlines all local CSS, JS, icons, and images into the
// HTML document as self-contained data URIs. Used by the -inline-assets mode.
func inlineLocalAssets(doc, srcRoot string) (string, error) {
	var err error

	doc, err = inlineLocalStylesheets(doc, srcRoot)
	if err != nil {
		return "", err
	}
	doc, err = inlineLocalScripts(doc, srcRoot)
	if err != nil {
		return "", err
	}
	doc, err = inlineLocalIcons(doc, srcRoot)
	if err != nil {
		return "", err
	}
	return inlineLocalImages(doc, srcRoot)
}

func wrapInStyleTag(css, media string) string {
	css = minifyCSS(css)
	if media != "" {
		return "<style media=\"" + htmlEscape(media) + "\">\n" + css + "\n</style>"
	}
	return "<style>\n" + css + "\n</style>"
}

// minifyCSS strips comments and collapses whitespace in CSS text. It uses a
// placeholder strategy to protect url() content, quoted string literals, and
// /*! preserved comments from being affected by whitespace transforms.
func minifyCSS(css string) string {
	if css == "" {
		return css
	}

	type placeholder struct{ token, original string }
	var slots []placeholder
	nextToken := func(original string) string {
		tok := fmt.Sprintf("__CSSPH%d__", len(slots))
		slots = append(slots, placeholder{tok, original})
		return tok
	}

	// Protect /*! preserved comments */ (license headers etc.).
	css = cssPreservedCommentRE.ReplaceAllStringFunc(css, func(m string) string {
		return nextToken(m)
	})

	// Strip regular /* ... */ block comments.
	css = cssBlockCommentRE.ReplaceAllString(css, "")

	// Protect url(...) content FIRST — url() may contain quoted strings (e.g.
	// url("fonts/x.woff2")) which must not be split by the string-literal pass.
	css = cssURLRE.ReplaceAllStringFunc(css, func(m string) string {
		return nextToken(m)
	})

	// Protect remaining quoted CSS string literals before structural transforms.
	// Without this, `content: "Price: free"` becomes `content:"Price:free"`
	// because cssAroundStructRE strips spaces around ':' and ','.
	css = cssDQStringRE.ReplaceAllStringFunc(css, func(m string) string {
		return nextToken(m)
	})
	css = cssSQStringRE.ReplaceAllStringFunc(css, func(m string) string {
		return nextToken(m)
	})

	// Collapse runs of whitespace (including newlines) to a single space.
	css = cssWhitespaceRE.ReplaceAllString(css, " ")

	// Strip spaces around structural characters.
	css = cssAroundStructRE.ReplaceAllStringFunc(css, func(m string) string {
		return strings.TrimSpace(m)
	})

	// Remove trailing semicolons before closing braces.
	css = strings.ReplaceAll(css, ";}", "}")

	css = strings.TrimSpace(css)

	// Restore placeholders.
	for _, s := range slots {
		css = strings.ReplaceAll(css, s.token, s.original)
	}
	return css
}

func inlineLocalStylesheets(doc, srcRoot string) (string, error) {
	return replaceTagWith(doc, stylesheetTagRE, func(tag string, refs []string) (string, error) {
		href := refs[1]
		if isExternalRef(href) {
			return tag, nil
		}
		cssBytes, cssPath, err := readAsset(srcRoot, href)
		if err != nil {
			return "", err
		}
		// Flatten @imports and convert url() refs to data URIs.
		inlinedCSS, err := flattenCSSImports(string(cssBytes), cssPath, srcRoot, nil, cssInlineOpts{preserveURLs: false})
		if err != nil {
			return "", err
		}
		return wrapInStyleTag(inlinedCSS, getTagAttr(tag, "media")), nil
	})
}

// inlineLocalStylesheetsPreserveURLs inlines CSS text and rewrites url()
// references to root-relative absolute paths (/dir/file.ext). This keeps font
// and image files external so they can be cached and fingerprinted separately,
// avoiding the ~280 KB of base64 font data that full inlining would embed per
// page. Local @import rules are flattened recursively. When embedFonts is true,
// font file url() refs are embedded as base64 data URIs instead.
func inlineLocalStylesheetsPreserveURLs(doc, srcRoot string, embedFonts bool) (string, error) {
	inlineOpts := cssInlineOpts{preserveURLs: true, embedFonts: embedFonts}
	return replaceTagWith(doc, stylesheetTagRE, func(tag string, refs []string) (string, error) {
		href := refs[1]
		if isExternalRef(href) {
			return tag, nil
		}
		cssBytes, cssPath, err := readAsset(srcRoot, href)
		if err != nil {
			return "", err
		}
		rebasedCSS, err := flattenCSSImports(string(cssBytes), cssPath, srcRoot, nil, inlineOpts)
		if err != nil {
			return "", err
		}
		return wrapInStyleTag(rebasedCSS, getTagAttr(tag, "media")), nil
	})
}

func inlineLocalScripts(doc, srcRoot string) (string, error) {
	return replaceTagWith(doc, scriptTagRE, func(tag string, refs []string) (string, error) {
		src := refs[1]
		if isExternalRef(src) {
			return tag, nil
		}
		jsBytes, _, err := readAsset(srcRoot, src)
		if err != nil {
			return "", err
		}
		return "<script>\n" + string(jsBytes) + "\n</script>", nil
	})
}

func inlineLocalIcons(doc, srcRoot string) (string, error) {
	return replaceTagWith(doc, iconTagRE, func(tag string, refs []string) (string, error) {
		href := refs[1]
		if isExternalRef(href) {
			return tag, nil
		}
		dataURL, err := assetToDataURL(srcRoot, href)
		if err != nil {
			return "", err
		}
		return strings.Replace(tag, href, dataURL, 1), nil
	})
}

func inlineLocalImages(doc, srcRoot string) (string, error) {
	return replaceTagWith(doc, imgTagRE, func(tag string, refs []string) (string, error) {
		src := refs[1]
		if isExternalRef(src) {
			return tag, nil
		}
		dataURL, err := assetToDataURL(srcRoot, src)
		if err != nil {
			return "", err
		}
		return strings.Replace(tag, src, dataURL, 1), nil
	})
}

// inlineSmallLocalRasterImages replaces <img src="..."> tags that reference local
// raster files smaller than threshold bytes with inline base64 data URIs. SVG
// files and images whose src is already a data URI (e.g. LQIP placeholders) are
// skipped. Runs after LQIP and before fingerprinting so the isDataURI guard
// correctly excludes LQIP-processed images.
func inlineSmallLocalRasterImages(doc, assetsDir string, threshold int) (string, error) {
	return replaceTagWith(doc, imgTagRE, func(tag string, refs []string) (string, error) {
		src := refs[1]
		if isExternalRef(src) || isDataURI(src) {
			return tag, nil
		}
		if strings.ToLower(path.Ext(strings.SplitN(src, "?", 2)[0])) == ".svg" {
			return tag, nil // SVGs handled separately by inlineLocalSVGs
		}
		data, _, err := readAsset(assetsDir, src)
		if err != nil || len(data) >= threshold {
			return tag, nil // not found or too large: leave for fingerprinting
		}
		dataURL, err := assetToDataURL(assetsDir, src)
		if err != nil {
			return tag, nil
		}
		return strings.Replace(tag, src, dataURL, 1), nil
	})
}

// inlineSmallLocalScripts replaces <script src="..."> tags that reference local
// files smaller than threshold bytes with inline <script> blocks, eliminating
// the HTTP request. Scripts at or above threshold stay external and are
// fingerprinted. type="module" scripts are always kept external because inline
// modules have different scoping semantics and cannot use import statements.
func inlineSmallLocalScripts(doc, assetsDir string, threshold int) (string, error) {
	return replaceTagWith(doc, scriptTagRE, func(tag string, refs []string) (string, error) {
		src := refs[1]
		if isExternalRef(src) {
			return tag, nil
		}
		if strings.EqualFold(getTagAttr(tag, "type"), "module") {
			return tag, nil
		}
		data, _, err := readAsset(assetsDir, src)
		if err != nil || len(data) >= threshold {
			return tag, nil // not found or too large: leave for fingerprinting
		}
		return "<script>\n" + string(data) + "\n</script>", nil
	})
}

// ── CSS URL and @import rewriting ─────────────────────────────────────────────

func inlineCSSURLs(cssText, srcRoot, cssPath string) (string, error) {
	return rewriteCSSURLs(cssText, func(assetRef string) (string, bool, error) {
		if isExternalRef(assetRef) || strings.HasPrefix(strings.ToLower(strings.TrimSpace(assetRef)), "data:") {
			return assetRef, false, nil
		}

		resolved := resolveCSSAssetRef(cssPath, assetRef)
		dataURL, err := assetToDataURL(srcRoot, resolved)
		if err != nil {
			return "", false, err
		}
		return dataURL, true, nil
	})
}

func rewriteCSSURLs(cssText string, replacer func(ref string) (replacement string, changed bool, err error)) (string, error) {
	matches := cssURLRE.FindAllStringSubmatchIndex(cssText, -1)
	if len(matches) == 0 {
		return cssText, nil
	}

	var out strings.Builder
	last := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		urlStart, urlEnd := m[2], m[3]
		out.WriteString(cssText[last:start])

		assetRef := strings.TrimSpace(cssText[urlStart:urlEnd])
		replacement, changed, err := replacer(assetRef)
		if err != nil {
			return "", err
		}
		if !changed {
			out.WriteString(cssText[start:end])
			last = end
			continue
		}

		out.WriteString("url(\"" + replacement + "\")")
		last = end
	}
	out.WriteString(cssText[last:])
	return out.String(), nil
}

func rewriteCSSImports(cssText string, replacer func(ref string) (replacement string, changed bool, err error)) (string, error) {
	matches := cssImportRE.FindAllStringSubmatchIndex(cssText, -1)
	if len(matches) == 0 {
		return cssText, nil
	}

	var out strings.Builder
	last := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		refStart, refEnd := m[2], m[3]
		suffixStart, suffixEnd := m[4], m[5]
		out.WriteString(cssText[last:start])

		ref := strings.TrimSpace(cssText[refStart:refEnd])
		replacement, changed, err := replacer(ref)
		if err != nil {
			return "", err
		}
		if !changed {
			out.WriteString(cssText[start:end])
			last = end
			continue
		}

		suffix := ""
		if suffixStart >= 0 && suffixEnd >= 0 {
			suffix = cssText[suffixStart:suffixEnd]
		}
		out.WriteString("@import url(\"" + replacement + "\")" + suffix + ";")
		last = end
	}
	out.WriteString(cssText[last:])
	return out.String(), nil
}

func resolveCSSAssetRef(cssPath, ref string) string {
	if strings.HasPrefix(ref, "/") {
		return strings.TrimPrefix(ref, "/")
	}
	base := filepath.Dir(cssPath)
	return filepath.ToSlash(filepath.Clean(filepath.Join(base, ref)))
}

// flattenCSSImports recursively expands local @import statements in cssText
// into the referenced file's content, applying url() rewriting as it goes.
// External @import rules (CDN fonts, etc.) are left verbatim. Missing files are
// skipped gracefully (the @import remains as-is). The seen map is shared across
// recursive calls so each file is included at most once (CSS deduplication
// semantics); pass nil for the initial call.
func flattenCSSImports(cssText, cssPath, srcRoot string, seen map[string]bool, opts cssInlineOpts) (string, error) {
	if seen == nil {
		seen = make(map[string]bool)
	}
	if seen[cssPath] {
		return "", nil // already included: deduplicate
	}
	seen[cssPath] = true

	matches := cssImportRE.FindAllStringSubmatchIndex(cssText, -1)
	if len(matches) == 0 {
		return rewriteCSSURLsForPath(cssText, cssPath, srcRoot, opts)
	}

	var out strings.Builder
	last := 0
	for _, m := range matches {
		prefix := cssText[last:m[0]]
		rewritten, err := rewriteCSSURLsForPath(prefix, cssPath, srcRoot, opts)
		if err != nil {
			return "", err
		}
		out.WriteString(rewritten)

		ref := strings.TrimSpace(cssText[m[2]:m[3]])
		expanded, err := expandCSSImport(cssText[m[0]:m[1]], ref, cssPath, srcRoot, seen, opts)
		if err != nil {
			return "", err
		}
		out.WriteString(expanded)
		last = m[1]
	}

	remaining := cssText[last:]
	rewritten, err := rewriteCSSURLsForPath(remaining, cssPath, srcRoot, opts)
	if err != nil {
		return "", err
	}
	out.WriteString(rewritten)
	return out.String(), nil
}

// expandCSSImport returns the replacement text for a single @import rule.
// For external imports it returns the original rule unchanged. For local imports
// it returns the recursively flattened file content, or the original rule if the
// file cannot be read.
func expandCSSImport(originalRule, ref, cssPath, srcRoot string, seen map[string]bool, opts cssInlineOpts) (string, error) {
	if isExternalRef(ref) || isDataURI(ref) {
		// External @import (CDN fonts, etc.) — leave verbatim; browsers resolve it.
		return originalRule, nil
	}
	importedPath := resolveCSSAssetRef(cssPath, ref)
	importedBytes, importedCleanPath, err := readAsset(srcRoot, importedPath)
	if err != nil {
		return originalRule, nil // not found: leave @import as-is rather than failing
	}
	return flattenCSSImports(string(importedBytes), importedCleanPath, srcRoot, seen, opts)
}

// cssInlineOpts controls how url() references are rewritten when CSS is inlined.
type cssInlineOpts struct {
	// preserveURLs: when true, url() becomes a root-relative path (/dir/file.ext).
	// When false, the file is read and embedded as a base64 data URI.
	preserveURLs bool
	// embedFonts: when true and preserveURLs is true, font file url() refs are
	// embedded as base64 data URIs instead of root-relative paths, eliminating
	// font files from the dist output.
	embedFonts bool
}

// rewriteCSSURLsForPath applies url() rewriting to cssText using cssPath for
// relative reference resolution. Behaviour is controlled by opts.
func rewriteCSSURLsForPath(cssText, cssPath, srcRoot string, opts cssInlineOpts) (string, error) {
	if opts.preserveURLs {
		return rewriteCSSURLs(cssText, func(ref string) (string, bool, error) {
			if isExternalRef(ref) || isDataURI(ref) {
				return ref, false, nil
			}
			resolved := resolveCSSAssetRef(cssPath, ref)
			if opts.embedFonts && isFontRef(resolved) {
				dataURL, err := assetToDataURL(srcRoot, resolved)
				if err != nil {
					return "", false, err
				}
				return dataURL, true, nil
			}
			abs := "/" + resolved
			return abs, abs != ref, nil
		})
	}
	return inlineCSSURLs(cssText, srcRoot, cssPath)
}

// isFontRef reports whether the asset reference points to a font file.
func isFontRef(ref string) bool {
	ext := strings.ToLower(path.Ext(strings.SplitN(ref, "?", 2)[0]))
	switch ext {
	case ".woff2", ".woff", ".ttf", ".otf", ".eot":
		return true
	}
	return false
}
