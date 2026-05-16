package buildcmd

import (
	"fmt"
	"path"
	"strings"
)

// injectNavigationEnhancements inserts two <head> elements into every rendered page:
//   - Cross-document view-transition CSS: eliminates the flash-of-unstyled-content on
//     page navigation by fading between pages instead of doing a hard repaint.
//   - Speculation Rules JSON: tells supporting browsers to prerender same-origin pages
//     the user is likely to visit (on hover, eagerness=moderate), making navigations
//     feel near-instant.
//
// Both features degrade silently in unsupported browsers, so no flag is provided.
func injectNavigationEnhancements(html string) string {
	const inject = `<style>@view-transition{navigation:auto}</style>` +
		"\n    " +
		`<script type="speculationrules">{"prerender":[{"where":{"href_matches":"/*"},"eagerness":"moderate"}]}</script>`
	return strings.Replace(html, "</head>", inject+"\n</head>", 1)
}

func transformPage(html string, opts buildOptions, assetsDir string, mirrorer *externalAssetMirrorer) (string, map[string]string, error) {
	html = injectNavigationEnhancements(html)

	if opts.inlineAssets {
		// Full asset inlining (CSS + JS + images). Converts url() to data URIs so the
		// page is fully self-contained. Does not fingerprint (data URIs need no cache key).
		updated, err := inlineLocalAssets(html, assetsDir)
		if err != nil {
			return "", nil, fmt.Errorf("inlining assets: %w", err)
		}
		html = updated
	} else {
		updated, err := applyPositionBasedTransforms(html, opts, assetsDir)
		if err != nil {
			return "", nil, err
		}
		html = updated
	}

	// LQIP: generate blurry thumbnails for above-fold images and swap to full on load.
	lqipHTML, err := processLQIPImages(html, assetsDir)
	if err != nil {
		return "", nil, fmt.Errorf("processing LQIP images: %w", err)
	}
	html = lqipHTML

	// Inline small raster images as base64 data URIs. Runs after LQIP so that
	// LQIP-processed images (whose src is already a data URI) are correctly skipped.
	if opts.rasterInlineThreshold > 0 {
		inlinedHTML, err := inlineSmallLocalRasterImages(html, assetsDir, opts.rasterInlineThreshold)
		if err != nil {
			return "", nil, fmt.Errorf("inlining small raster images: %w", err)
		}
		html = inlinedHTML
	}

	// Fingerprint all remaining external local asset references so CloudFront
	// can serve them with immutable (1-year) cache headers.
	fingerprintedHTML, toCopy, err := fingerprintLocalAssets(html, assetsDir)
	if err != nil {
		return "", nil, fmt.Errorf("fingerprinting assets: %w", err)
	}
	html = fingerprintedHTML

	if mirrorer != nil {
		updated, err := mirrorer.rewriteHTML(html)
		if err != nil {
			return "", nil, fmt.Errorf("mirroring external assets: %w", err)
		}
		html = updated
	}

	return html, toCopy, nil
}

// applyPositionBasedTransforms is the default asset transform path (used when
// -inline-assets is not set). It applies:
//   - Position-based CSS loading: head CSS inlined as <style>, body CSS deferred.
//   - SVG icon inlining for icons below svgInlineSizeLimit.
//   - JS inlining for scripts below opts.jsInlineThreshold (when > 0).
func applyPositionBasedTransforms(html string, opts buildOptions, assetsDir string) (string, error) {
	// Position-based CSS: head CSS → <style> (critical), body CSS → deferred external
	// (or inlined when -inline-body-css is set).
	updated, err := transformStylesheets(html, assetsDir, opts.embedFonts, opts.inlineBodyCSS)
	if err != nil {
		return "", fmt.Errorf("transforming stylesheets: %w", err)
	}
	html = updated

	// Inline small SVGs as <svg> elements to eliminate per-icon HTTP requests.
	updated, err = inlineLocalSVGs(html, assetsDir)
	if err != nil {
		return "", fmt.Errorf("inlining SVGs: %w", err)
	}
	html = updated

	// Inline scripts below the size threshold to eliminate per-script HTTP requests.
	if opts.jsInlineThreshold > 0 {
		updated, err = inlineSmallLocalScripts(html, assetsDir, opts.jsInlineThreshold)
		if err != nil {
			return "", fmt.Errorf("inlining small scripts: %w", err)
		}
		html = updated
	}
	return html, nil
}

// transformStylesheets applies position-based CSS loading: stylesheet links in <head>
// are inlined (critical path, zero-latency), while links in <body> are kept external
// and given the media="print" onload deferred-loading pattern (non-blocking).
//
// This mirrors the JS-at-end convention: document position signals loading priority.
// Templates that want a stylesheet deferred simply place its <link> in the body.
// If </head> is absent the entire document is treated as head (all CSS inlined).
func transformStylesheets(doc, assetsDir string, embedFonts, inlineBodyCSS bool) (string, error) {
	loc := headEndRE.FindStringIndex(doc)
	if loc == nil {
		return inlineLocalStylesheetsPreserveURLs(doc, assetsDir, embedFonts)
	}
	head, err := inlineLocalStylesheetsPreserveURLs(doc[:loc[0]], assetsDir, embedFonts)
	if err != nil {
		return "", err
	}
	var tail string
	if inlineBodyCSS {
		tail, err = inlineLocalStylesheetsPreserveURLs(doc[loc[0]:], assetsDir, embedFonts)
	} else {
		tail, err = deferLocalStylesheets(doc[loc[0]:], assetsDir)
	}
	if err != nil {
		return "", err
	}
	return head + tail, nil
}

// deferLocalStylesheets rewrites local <link rel="stylesheet"> tags found in the
// document body to use the media="print" onload deferred-loading pattern, which
// fetches the CSS without blocking rendering. A <noscript> fallback is appended after
// each transformed tag for environments where JavaScript is unavailable.
// External stylesheet URLs are left unchanged.
func deferLocalStylesheets(doc, assetsDir string) (string, error) {
	return replaceTagWith(doc, stylesheetTagRE, func(tag string, refs []string) (string, error) {
		href := refs[1]
		if isExternalRef(href) {
			return tag, nil
		}
		// Verify the asset exists; skip if not found.
		if _, _, err := readAsset(assetsDir, href); err != nil {
			return tag, nil
		}
		deferred := addOrReplaceAttr(tag, "media", "print")
		deferred = addOrReplaceAttr(deferred, "onload", "this.media='all'")
		noscript := `<noscript>` + tag + `</noscript>`
		return deferred + "\n" + noscript, nil
	})
}

// inlineLocalSVGs replaces <img src="*.svg"> tags that reference small local
// SVG files with the SVG XML content inline, eliminating the HTTP request for
// the icon. SVGs >= svgInlineSizeLimit are left for normal fingerprinting.
//
// Runs in the default build pipeline after CSS transforms and before LQIP and
// fingerprinting, so that inlined SVGs are not double-processed. Skipped in
// -inline-assets mode where inlineLocalImages already handles SVGs as data URIs.
func inlineLocalSVGs(doc, assetsDir string) (string, error) {
	return replaceTagWith(doc, imgTagRE, func(tag string, refs []string) (string, error) {
		src := refs[1]
		if isExternalRef(src) {
			return tag, nil
		}
		if strings.ToLower(path.Ext(strings.SplitN(src, "?", 2)[0])) != ".svg" {
			return tag, nil
		}
		data, _, err := readAsset(assetsDir, src)
		if err != nil || len(data) >= svgInlineSizeLimit {
			return tag, nil // not found or too large: leave for fingerprinting
		}
		return svgFromImgTag(tag, data), nil
	})
}

// svgFromImgTag prepares raw SVG bytes for inline HTML embedding:
//   - strips the <?xml ...?> processing instruction (invalid in HTML5)
//   - merges the img's class attribute into the <svg> root element's class
//   - converts a non-empty alt to aria-label + role="img" on <svg>;
//     an empty alt becomes aria-hidden="true" (decorative icon)
//   - copies width/height from the img when the <svg> root lacks them
func svgFromImgTag(imgTag string, svgData []byte) string {
	svg := strings.TrimSpace(xmlProcInstRE.ReplaceAllString(string(svgData), ""))

	loc := svgRootTagRE.FindStringIndex(svg)
	if loc == nil {
		return svg
	}

	root := svg[loc[0]:loc[1]]
	extra := ""

	imgClass := getTagAttr(imgTag, "class")
	imgAlt := getTagAttr(imgTag, "alt")
	imgWidth := getTagAttr(imgTag, "width")
	imgHeight := getTagAttr(imgTag, "height")

	if imgClass != "" {
		if existing := getTagAttr(root, "class"); existing != "" {
			root = strings.Replace(root, `class="`+existing+`"`, `class="`+imgClass+` `+existing+`"`, 1)
		} else {
			extra += ` class="` + imgClass + `"`
		}
	}
	if imgAlt != "" {
		// imgAlt is the raw attribute value extracted from HTML — already entity-encoded
		// by the template engine. Only escape " to avoid breaking the aria-label delimiter.
		extra += ` aria-label="` + strings.ReplaceAll(imgAlt, `"`, "&quot;") + `" role="img"`
	} else if getTagAttr(root, "aria-hidden") == "" {
		extra += ` aria-hidden="true"`
	}
	if imgWidth != "" && getTagAttr(root, "width") == "" {
		extra += ` width="` + imgWidth + `"`
	}
	if imgHeight != "" && getTagAttr(root, "height") == "" {
		extra += ` height="` + imgHeight + `"`
	}

	if extra != "" {
		if strings.HasSuffix(root, "/>") {
			root = root[:len(root)-2] + extra + "/>"
		} else {
			root = root[:len(root)-1] + extra + ">"
		}
	}

	return svg[:loc[0]] + root + svg[loc[1]:]
}
