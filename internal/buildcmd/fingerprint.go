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
// "portrait.webp" + "a1b2c3d4" → "portrait.a1b2c3d4.webp".
func insertHashInPath(relPath, hash string) string {
	ext := path.Ext(relPath)
	return relPath[:len(relPath)-len(ext)] + "." + hash + ext
}

// isDataURI reports whether ref is a data: URI.
func isDataURI(ref string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(ref)), "data:")
}

// assetFingerprinter holds shared state for a single fingerprintLocalAssets
// call. It avoids threading hashCache, toCopy, normalizedBase, and assetsDir
// through every closure and helper, reducing per-function complexity.
type assetFingerprinter struct {
	assetsDir      string
	normalizedBase string
	hashCache      map[string]string // cleanRelPath → 8-char hash
	toCopy         map[string]string // hashedRelPath → originalRelPath
}

// resolve returns the content-hashed path for ref, recording the original →
// hashed mapping in fp.toCopy. External refs, data URIs, and missing assets
// are returned unchanged.
func (fp *assetFingerprinter) resolve(ref string) (string, error) {
	if ref == "" || isExternalRef(ref) || isDataURI(ref) {
		return ref, nil
	}
	data, cleanRef, err := readAsset(fp.assetsDir, ref)
	if err != nil {
		return ref, nil // asset not found — leave as-is
	}
	hash, ok := fp.hashCache[cleanRef]
	if !ok {
		hash = assetContentHash(data)
		fp.hashCache[cleanRef] = hash
	}
	hashed := insertHashInPath(cleanRef, hash)
	fp.toCopy[hashed] = cleanRef
	if strings.HasPrefix(ref, "/") {
		return fp.normalizedBase + "/" + hashed, nil
	}
	return hashed, nil
}

// rewriteAttr replaces attr="oldVal" (or attr='oldVal') in tag with
// attr="newVal". Returns tag unchanged when oldVal == newVal.
func (fp *assetFingerprinter) rewriteAttr(tag, attr, oldVal, newVal string) string {
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

// fingerprintHrefTags rewrites href="..." in all tags matched by re.
func (fp *assetFingerprinter) fingerprintHrefTags(html string, re *regexp.Regexp) (string, error) {
	return replaceTagWith(html, re, func(tag string, refs []string) (string, error) {
		href := refs[1]
		newHref, e := fp.resolve(href)
		return fp.rewriteAttr(tag, "href", href, newHref), e
	})
}

// fingerprintDataSrc rewrites data-src="..." attributes written by LQIP processing.
func (fp *assetFingerprinter) fingerprintDataSrc(html string) (string, error) {
	var firstErr error
	out := dataSrcAttrRE.ReplaceAllStringFunc(html, func(m string) string {
		if firstErr != nil {
			return m
		}
		parts := dataSrcAttrRE.FindStringSubmatch(m)
		if parts == nil {
			return m
		}
		newRef, e := fp.resolve(parts[2])
		if e != nil {
			firstErr = e
			return m
		}
		return parts[1] + newRef + parts[3]
	})
	return out, firstErr
}

// fingerprintStyleBlocks rewrites url() references inside inline <style> blocks.
func (fp *assetFingerprinter) fingerprintStyleBlocks(html string) (string, error) {
	var firstErr error
	out := styleBlockRE.ReplaceAllStringFunc(html, func(block string) string {
		if firstErr != nil {
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
			newRef, e2 := fp.resolve(ref)
			return newRef, newRef != ref, e2
		})
		if e != nil {
			firstErr = e
			return block
		}
		return openTag + newCSS + closeTag
	})
	return out, firstErr
}

// fingerprintLocalAssets rewrites all local asset references in html to use
// content-hashed filenames (e.g. "portrait.a1b2c3d4.webp") so the packer
// can serve them with the immutable cache tier. Returns the rewritten html
// and a mapping of hashedRelPath → originalRelPath (relative to assetsDir)
// for callers to write the hashed copies to the output directory.
//
// CSS url() inside inline <style> blocks is also rewritten. Data URIs and
// external URLs are left unchanged.
//
// basePath is prepended to root-absolute asset references (those starting with
// "/") so they remain reachable when the site is deployed under a path prefix
// like "/en". Empty for root-served sites. Relative references are untouched —
// they resolve correctly against the document's URL regardless of base path.
func fingerprintLocalAssets(html, assetsDir, basePath string) (string, map[string]string, error) {
	fp := &assetFingerprinter{
		assetsDir:      assetsDir,
		normalizedBase: strings.TrimRight(basePath, "/"),
		hashCache:      make(map[string]string),
		toCopy:         make(map[string]string),
	}

	var err error

	// <img src="..."> — skip data URIs placed there by LQIP processing
	html, err = replaceTagWith(html, imgTagRE, func(tag string, refs []string) (string, error) {
		src := refs[1]
		if isDataURI(src) {
			return tag, nil
		}
		newSrc, e := fp.resolve(src)
		return fp.rewriteAttr(tag, "src", src, newSrc), e
	})
	if err != nil {
		return "", nil, fmt.Errorf("fingerprinting img src: %w", err)
	}

	// <img data-src="..."> written by LQIP processing
	if html, err = fp.fingerprintDataSrc(html); err != nil {
		return "", nil, fmt.Errorf("fingerprinting data-src: %w", err)
	}

	// <link rel="preload" href="...">, <link rel="icon" href="...">, <link rel="manifest" href="...">
	for _, step := range []struct {
		re  *regexp.Regexp
		ctx string
	}{
		{preloadTagRE, "fingerprinting preload href"},
		{iconTagRE, "fingerprinting icon href"},
		{manifestTagRE, "fingerprinting manifest href"},
	} {
		if html, err = fp.fingerprintHrefTags(html, step.re); err != nil {
			return "", nil, fmt.Errorf("%s: %w", step.ctx, err)
		}
	}

	// <script src="...">
	html, err = replaceTagWith(html, scriptTagRE, func(tag string, refs []string) (string, error) {
		src := refs[1]
		newSrc, e := fp.resolve(src)
		return fp.rewriteAttr(tag, "src", src, newSrc), e
	})
	if err != nil {
		return "", nil, fmt.Errorf("fingerprinting script src: %w", err)
	}

	// url() inside inline <style> blocks (font @font-face src, background-image, etc.)
	if html, err = fp.fingerprintStyleBlocks(html); err != nil {
		return "", nil, fmt.Errorf("fingerprinting CSS url(): %w", err)
	}

	return html, fp.toCopy, nil
}
