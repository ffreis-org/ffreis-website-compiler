package buildcmd

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"strings"
)

var (
	styleBlockRE  = regexp.MustCompile(`(?is)(<style\b[^>]*>)(.*?)(</style\s*>)`)
	dataSrcAttrRE = regexp.MustCompile(`(?i)(data-src=["'])([^"']+)(["'])`)
)

// assetContentHash returns the first 8 hex chars of SHA-256 of data.
// This matches the packer's hashedAssetToken pattern: [._-][a-f0-9]{8,}[._-].
func assetContentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:4]) // 4 bytes = 8 hex chars
}

// insertHashInPath inserts a hash token before the last extension.
// "portrait.webp" + "a1b2c3d4" → "portrait.a1b2c3d4.webp"
func insertHashInPath(relPath, hash string) string {
	ext := path.Ext(relPath)
	return relPath[:len(relPath)-len(ext)] + "." + hash + ext
}

// isDataURI reports whether ref is a data: URI.
func isDataURI(ref string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(ref)), "data:")
}

// fingerprintLocalAssets rewrites all local asset references in html to use
// content-hashed filenames (e.g. "portrait.a1b2c3d4.webp") so the packer
// can serve them with the immutable cache tier. Returns the rewritten html
// and a mapping of hashedRelPath → originalRelPath (relative to assetsDir)
// for callers to write the hashed copies to the output directory.
//
// CSS url() inside inline <style> blocks is also rewritten. Data URIs and
// external URLs are left unchanged.
func fingerprintLocalAssets(html, assetsDir string) (string, map[string]string, error) {
	hashCache := make(map[string]string) // cleanRelPath → 8-char hash
	toCopy := make(map[string]string)    // hashedRelPath → originalRelPath

	resolve := func(ref string) (string, error) {
		if ref == "" || isExternalRef(ref) || isDataURI(ref) {
			return ref, nil
		}
		data, cleanRef, err := readAsset(assetsDir, ref)
		if err != nil {
			return ref, nil // asset not found — leave as-is
		}
		hash, ok := hashCache[cleanRef]
		if !ok {
			hash = assetContentHash(data)
			hashCache[cleanRef] = hash
		}
		hashed := insertHashInPath(cleanRef, hash)
		toCopy[hashed] = cleanRef
		if strings.HasPrefix(ref, "/") {
			return "/" + hashed, nil
		}
		return hashed, nil
	}

	rewriteAttr := func(tag, attr, oldVal, newVal string) string {
		if oldVal == newVal {
			return tag
		}
		for _, q := range []string{`"`, `'`} {
			placeholder := attr + "=" + q + oldVal + q
			if strings.Contains(tag, placeholder) {
				return strings.Replace(tag, placeholder, attr+"="+q+newVal+q, 1)
			}
		}
		return tag
	}

	var err error

	// <img src="..."> — skip data URIs placed there by LQIP processing
	html, err = replaceTagWith(html, imgTagRE, func(tag string, refs []string) (string, error) {
		src := refs[1]
		if isDataURI(src) {
			return tag, nil
		}
		newSrc, e := resolve(src)
		return rewriteAttr(tag, "src", src, newSrc), e
	})
	if err != nil {
		return "", nil, fmt.Errorf("fingerprinting img src: %w", err)
	}

	// <img data-src="..."> written by LQIP processing
	html = dataSrcAttrRE.ReplaceAllStringFunc(html, func(m string) string {
		if err != nil {
			return m
		}
		parts := dataSrcAttrRE.FindStringSubmatch(m)
		if parts == nil {
			return m
		}
		newRef, e := resolve(parts[2])
		if e != nil {
			err = e
			return m
		}
		return parts[1] + newRef + parts[3]
	})
	if err != nil {
		return "", nil, fmt.Errorf("fingerprinting data-src: %w", err)
	}

	// <link rel="preload" href="...">
	html, err = replaceTagWith(html, preloadTagRE, func(tag string, refs []string) (string, error) {
		href := refs[1]
		newHref, e := resolve(href)
		return rewriteAttr(tag, "href", href, newHref), e
	})
	if err != nil {
		return "", nil, fmt.Errorf("fingerprinting preload href: %w", err)
	}

	// <link rel="icon" href="..."> and <link rel="apple-touch-icon" href="...">
	html, err = replaceTagWith(html, iconTagRE, func(tag string, refs []string) (string, error) {
		href := refs[1]
		newHref, e := resolve(href)
		return rewriteAttr(tag, "href", href, newHref), e
	})
	if err != nil {
		return "", nil, fmt.Errorf("fingerprinting icon href: %w", err)
	}

	// <link rel="manifest" href="...">
	html, err = replaceTagWith(html, manifestTagRE, func(tag string, refs []string) (string, error) {
		href := refs[1]
		newHref, e := resolve(href)
		return rewriteAttr(tag, "href", href, newHref), e
	})
	if err != nil {
		return "", nil, fmt.Errorf("fingerprinting manifest href: %w", err)
	}

	// <script src="...">
	html, err = replaceTagWith(html, scriptTagRE, func(tag string, refs []string) (string, error) {
		src := refs[1]
		newSrc, e := resolve(src)
		return rewriteAttr(tag, "src", src, newSrc), e
	})
	if err != nil {
		return "", nil, fmt.Errorf("fingerprinting script src: %w", err)
	}

	// url() inside inline <style> blocks (font @font-face src, background-image, etc.)
	html = styleBlockRE.ReplaceAllStringFunc(html, func(block string) string {
		if err != nil {
			return block
		}
		parts := styleBlockRE.FindStringSubmatch(block)
		if parts == nil {
			return block
		}
		openTag, css, closeTag := parts[1], parts[2], parts[3]
		newCSS, e := rewriteCSSURLs(css, func(ref string) (string, bool, error) {
			if isExternalRef(ref) || isDataURI(ref) {
				return ref, false, nil
			}
			newRef, e2 := resolve(ref)
			return newRef, newRef != ref, e2
		})
		if e != nil {
			err = e
			return block
		}
		return openTag + newCSS + closeTag
	})
	if err != nil {
		return "", nil, fmt.Errorf("fingerprinting CSS url(): %w", err)
	}

	return html, toCopy, nil
}
