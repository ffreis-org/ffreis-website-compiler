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

// assetResolver holds the shared state for resolving local asset references
// to their content-hashed equivalents during fingerprinting.
type assetResolver struct {
	assetsDir      string
	normalizedBase string
	hashCache      map[string]string // cleanRelPath → 8-char hash
	toCopy         map[string]string // hashedRelPath → originalRelPath
}

func newAssetResolver(assetsDir, basePath string) *assetResolver {
	return &assetResolver{
		assetsDir:      assetsDir,
		normalizedBase: strings.TrimRight(basePath, "/"),
		hashCache:      make(map[string]string),
		toCopy:         make(map[string]string),
	}
}

func (r *assetResolver) resolve(ref string) (string, error) {
	if ref == "" || isExternalRef(ref) || isDataURI(ref) {
		return ref, nil
	}
	data, cleanRef, err := readAsset(r.assetsDir, ref)
	if err != nil {
		return ref, nil // asset not found — leave as-is
	}
	hash, ok := r.hashCache[cleanRef]
	if !ok {
		hash = assetContentHash(data)
		r.hashCache[cleanRef] = hash
	}
	hashed := insertHashInPath(cleanRef, hash)
	r.toCopy[hashed] = cleanRef
	if strings.HasPrefix(ref, "/") {
		return r.normalizedBase + "/" + hashed, nil
	}
	return hashed, nil
}

// rewriteTagAttr replaces oldVal with newVal for the named attribute in tag.
func rewriteTagAttr(tag, attr, oldVal, newVal string) string {
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
	r := newAssetResolver(assetsDir, basePath)
	var err error

	// <img src="..."> — skip data URIs placed there by LQIP processing
	html, err = replaceTagWith(html, imgTagRE, func(tag string, refs []string) (string, error) {
		src := refs[1]
		if isDataURI(src) {
			return tag, nil
		}
		newSrc, e := r.resolve(src)
		return rewriteTagAttr(tag, "src", src, newSrc), e
	})
	if err != nil {
		return "", nil, fmt.Errorf("fingerprinting img src: %w", err)
	}

	html, err = fingerprintDataSrc(html, r)
	if err != nil {
		return "", nil, fmt.Errorf("fingerprinting data-src: %w", err)
	}

	// Simple href-attribute tags: preload, icon, manifest, script src.
	type hrefTag struct {
		re   *regexp.Regexp
		attr string
		desc string
	}
	for _, ht := range []hrefTag{
		{preloadTagRE, "href", "preload href"},
		{iconTagRE, "href", "icon href"},
		{manifestTagRE, "href", "manifest href"},
		{scriptTagRE, "src", "script src"},
	} {
		html, err = replaceTagWith(html, ht.re, func(tag string, refs []string) (string, error) {
			old := refs[1]
			newVal, e := r.resolve(old)
			return rewriteTagAttr(tag, ht.attr, old, newVal), e
		})
		if err != nil {
			return "", nil, fmt.Errorf("fingerprinting %s: %w", ht.desc, err)
		}
	}

	html, err = fingerprintStyleBlocks(html, r)
	if err != nil {
		return "", nil, fmt.Errorf("fingerprinting CSS url(): %w", err)
	}

	return html, r.toCopy, nil
}

// fingerprintDataSrc rewrites data-src attributes written by LQIP processing.
func fingerprintDataSrc(html string, r *assetResolver) (string, error) {
	var outerErr error
	html = dataSrcAttrRE.ReplaceAllStringFunc(html, func(m string) string {
		if outerErr != nil {
			return m
		}
		parts := dataSrcAttrRE.FindStringSubmatch(m)
		if parts == nil {
			return m
		}
		newRef, e := r.resolve(parts[2])
		if e != nil {
			outerErr = e
			return m
		}
		return parts[1] + newRef + parts[3]
	})
	return html, outerErr
}

// fingerprintStyleBlocks rewrites url() references inside inline <style> blocks.
func fingerprintStyleBlocks(html string, r *assetResolver) (string, error) {
	var outerErr error
	html = styleBlockRE.ReplaceAllStringFunc(html, func(block string) string {
		if outerErr != nil {
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
			newRef, e2 := r.resolve(ref)
			return newRef, newRef != ref, e2
		})
		if e != nil {
			outerErr = e
			return block
		}
		return openTag + newCSS + closeTag
	})
	return html, outerErr
}
