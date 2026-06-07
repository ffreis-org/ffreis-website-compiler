package buildcmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

type externalAssetMirrorer struct {
	client     *http.Client
	outDir     string
	assetsDir  string
	cache      map[string]string
	inProgress map[string]string
}

func newExternalAssetMirrorer(outDir, assetsDir string) *externalAssetMirrorer {
	return &externalAssetMirrorer{
		client:     &http.Client{Timeout: 30 * time.Second},
		outDir:     outDir,
		assetsDir:  strings.Trim(strings.TrimSpace(filepath.ToSlash(assetsDir)), "/"),
		cache:      make(map[string]string),
		inProgress: make(map[string]string),
	}
}

func (m *externalAssetMirrorer) rewriteHTML(doc string) (string, error) {
	var err error

	doc, err = replaceTagWith(doc, stylesheetTagRE, func(tag string, refs []string) (string, error) {
		return m.replaceExternalRef(tag, refs[1], ".css")
	})
	if err != nil {
		return "", err
	}

	doc, err = replaceTagWith(doc, preloadTagRE, func(tag string, refs []string) (string, error) {
		return m.replaceExternalRef(tag, refs[1], hintedExtFromPreload(tag))
	})
	if err != nil {
		return "", err
	}

	doc, err = replaceTagWith(doc, scriptTagRE, func(tag string, refs []string) (string, error) {
		return m.replaceExternalRef(tag, refs[1], ".js")
	})
	if err != nil {
		return "", err
	}

	doc, err = replaceTagWith(doc, iconTagRE, func(tag string, refs []string) (string, error) {
		return m.replaceExternalRef(tag, refs[1], "")
	})
	if err != nil {
		return "", err
	}

	doc, err = replaceTagWith(doc, imgTagRE, func(tag string, refs []string) (string, error) {
		return m.replaceExternalRef(tag, refs[1], "")
	})
	if err != nil {
		return "", err
	}

	// Mirror external url() refs inside inline <style> blocks. These arise when CSS
	// is inlined by the compiler (inlineLocalStylesheetsPreserveURLs) and the original
	// CSS file contained external http:// background-image or other url() references.
	doc = styleBlockRE.ReplaceAllStringFunc(doc, func(block string) string {
		if err != nil {
			return block
		}
		parts := styleBlockRE.FindStringSubmatch(block)
		if parts == nil {
			return block
		}
		rewritten, e := m.rewriteCSS(parts[2], nil)
		if e != nil {
			err = e
			return block
		}
		return parts[1] + rewritten + parts[3]
	})
	if err != nil {
		return "", err
	}

	return doc, nil
}

func (m *externalAssetMirrorer) replaceExternalRef(tag, ref, hintedExt string) (string, error) {
	absoluteURL, ok := resolveExternalURL(nil, ref)
	if !ok {
		return tag, nil
	}
	localRef, err := m.mirrorURL(absoluteURL, hintedExt)
	if err != nil {
		return "", err
	}
	return strings.Replace(tag, ref, "/"+localRef, 1), nil
}

const maxMirroredAssetBytes = 100 * 1024 * 1024 // 100 MiB

func (m *externalAssetMirrorer) mirrorURL(absoluteURL, hintedExt string) (string, error) {
	if cached, ok := m.cache[absoluteURL]; ok {
		return cached, nil
	}
	if pending, ok := m.inProgress[absoluteURL]; ok {
		return pending, nil
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, absoluteURL, nil)
	if err != nil {
		return "", fmt.Errorf("creating request for external asset %s: %w", absoluteURL, err)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading external asset %s: %w", absoluteURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("downloading external asset %s: unexpected status %s", absoluteURL, resp.Status)
	}

	if resp.ContentLength > maxMirroredAssetBytes {
		return "", fmt.Errorf("external asset %s Content-Length (%d) exceeds maximum download size of %d bytes", absoluteURL, resp.ContentLength, maxMirroredAssetBytes)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMirroredAssetBytes+1))
	if err != nil {
		return "", fmt.Errorf("reading external asset %s: %w", absoluteURL, err)
	}
	if int64(len(body)) > maxMirroredAssetBytes {
		return "", fmt.Errorf("external asset %s exceeds maximum download size of %d bytes", absoluteURL, maxMirroredAssetBytes)
	}

	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	relPath := mirroredAssetRelPath(absoluteURL, contentType, hintedExt, m.assetsDir)
	m.inProgress[absoluteURL] = relPath
	defer delete(m.inProgress, absoluteURL)

	if isCSSContentType(contentType, relPath, hintedExt) {
		body, err = m.rewriteBodyAsCSS(absoluteURL, body)
		if err != nil {
			return "", err
		}
	}

	fullPath := filepath.Join(m.outDir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return "", fmt.Errorf("creating mirrored asset directory for %s: %w", absoluteURL, err)
	}
	if err := os.WriteFile(fullPath, body, 0o644); err != nil { //nolint:gosec
		return "", fmt.Errorf("writing mirrored asset %s: %w", absoluteURL, err)
	}

	m.cache[absoluteURL] = relPath
	return relPath, nil
}

func (m *externalAssetMirrorer) rewriteBodyAsCSS(absoluteURL string, body []byte) ([]byte, error) {
	baseURL, err := url.Parse(absoluteURL)
	if err != nil {
		return nil, fmt.Errorf("parsing css asset url %s: %w", absoluteURL, err)
	}
	rewritten, err := m.rewriteCSS(string(body), baseURL)
	if err != nil {
		return nil, err
	}
	return []byte(rewritten), nil
}

func (m *externalAssetMirrorer) rewriteCSS(cssText string, baseURL *url.URL) (string, error) {
	rewritten, err := rewriteCSSImports(cssText, func(ref string) (string, bool, error) {
		absoluteURL, ok := resolveExternalURL(baseURL, ref)
		if !ok {
			return ref, false, nil
		}
		localRef, err := m.mirrorURL(absoluteURL, ".css")
		if err != nil {
			return "", false, err
		}
		return "/" + localRef, true, nil
	})
	if err != nil {
		return "", err
	}

	return rewriteCSSURLs(rewritten, func(ref string) (string, bool, error) {
		absoluteURL, ok := resolveExternalURL(baseURL, ref)
		if !ok {
			return ref, false, nil
		}
		localRef, err := m.mirrorURL(absoluteURL, "")
		if err != nil {
			return "", false, err
		}
		return "/" + localRef, true, nil
	})
}

// ── URL resolution helpers ────────────────────────────────────────────────────

func resolveExternalURL(baseURL *url.URL, ref string) (string, bool) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return "", false
	}

	if strings.HasPrefix(trimmed, "//") {
		return resolveSchemeRelativeURL(baseURL, trimmed), true
	}

	if shouldSkipExternalURL(trimmed) {
		return "", false
	}
	if absoluteURL, ok := parseAbsoluteHTTPURL(trimmed); ok {
		return absoluteURL, true
	}
	if baseURL == nil {
		return "", false
	}

	return resolveRelativeHTTPURL(baseURL, trimmed)
}

func shouldSkipExternalURL(trimmedRef string) bool {
	lower := strings.ToLower(trimmedRef)
	switch {
	case strings.HasPrefix(lower, "data:"),
		strings.HasPrefix(lower, "mailto:"),
		strings.HasPrefix(lower, "tel:"),
		strings.HasPrefix(lower, "javascript:"),
		strings.HasPrefix(lower, "vbscript:"),
		strings.HasPrefix(lower, "#"):
		return true
	default:
		return false
	}
}

func resolveSchemeRelativeURL(baseURL *url.URL, schemeRelative string) string {
	scheme := "https"
	if baseURL != nil && baseURL.Scheme != "" {
		scheme = baseURL.Scheme
	}
	return scheme + ":" + schemeRelative
}

func parseAbsoluteHTTPURL(ref string) (string, bool) {
	parsed, err := url.Parse(ref)
	if err != nil {
		return "", false
	}
	if !parsed.IsAbs() {
		return "", false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false
	}
	return parsed.String(), true
}

func resolveRelativeHTTPURL(baseURL *url.URL, ref string) (string, bool) {
	resolved := baseURL.ResolveReference(&url.URL{Path: ref})
	if strings.Contains(ref, "?") || strings.Contains(ref, "#") {
		if parsed, err := url.Parse(ref); err == nil {
			resolved = baseURL.ResolveReference(parsed)
		}
	}
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return "", false
	}
	if resolved.Host == "" {
		return "", false
	}
	return resolved.String(), true
}

func hintedExtFromPreload(tag string) string {
	switch strings.ToLower(strings.TrimSpace(getTagAttr(tag, "as"))) {
	case "style":
		return ".css"
	case "script":
		return ".js"
	case "font":
		if hrefType := strings.ToLower(strings.TrimSpace(getTagAttr(tag, "type"))); hrefType != "" {
			return extensionFromContentType(hrefType)
		}
		return extWoff2
	case "image":
		return ""
	default:
		return ""
	}
}

func mirroredAssetRelPath(absoluteURL, contentType, hintedExt, assetsDir string) string {
	parsed, err := url.Parse(absoluteURL)
	if err != nil {
		sum := sha256.Sum256([]byte(absoluteURL))
		return path.Join(assetsDir, "unknown", hex.EncodeToString(sum[:8])+normalizeExt(hintedExt))
	}

	host := sanitizePathSegment(parsed.Host)
	segments := []string{}
	cleanPath := strings.Trim(parsed.Path, "/")
	if cleanPath != "" {
		for _, segment := range strings.Split(cleanPath, "/") {
			sanitized := sanitizePathSegment(segment)
			if sanitized != "" {
				segments = append(segments, sanitized)
			}
		}
	}
	if len(segments) == 0 {
		segments = []string{"index"}
	}

	fileName := segments[len(segments)-1]
	dirParts := segments[:len(segments)-1]
	ext := normalizeExt(filepath.Ext(fileName))
	if ext == "" {
		ext = normalizeExt(hintedExt)
	}
	if ext == "" {
		ext = extensionFromContentType(contentType)
	}
	if ext == "" {
		ext = ".bin"
	}

	fileStem := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	if fileStem == "" {
		fileStem = "index"
	}
	if parsed.RawQuery != "" {
		fileStem += "--" + shortHash(parsed.RawQuery)
	}

	parts := []string{assetsDir, host}
	parts = append(parts, dirParts...)
	parts = append(parts, fileStem+ext)
	return path.Join(parts...)
}

func sanitizePathSegment(v string) string {
	if v == "" {
		return ""
	}
	replacer := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "_",
		"?", "_",
		"&", "_",
		"=", "_",
		"%", "_",
		"+", "_",
	)
	v = replacer.Replace(v)
	v = strings.Trim(v, "._-")
	if v == "" {
		return "asset"
	}
	return v
}

func shortHash(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:6])
}

func normalizeExt(v string) string {
	if v == "" {
		return ""
	}
	if strings.HasPrefix(v, ".") {
		return strings.ToLower(v)
	}
	return "." + strings.ToLower(v)
}

func isCSSContentType(contentType, relPath, hintedExt string) bool {
	if strings.EqualFold(strings.TrimSpace(strings.Split(contentType, ";")[0]), mimeTextCSS) {
		return true
	}
	switch normalizeExt(filepath.Ext(relPath)) {
	case ".css":
		return true
	}
	return normalizeExt(hintedExt) == ".css"
}
