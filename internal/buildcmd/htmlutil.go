package buildcmd

import (
	"encoding/base64"
	"regexp"
	"strings"
	"sync"
)

// attrSearchCache and attrValueCache hold compiled regexes keyed by attribute name.
// Dynamic patterns are cached so each unique attr name pays the compilation cost only once.
var (
	attrSearchCache sync.Map // attr → *regexp.Regexp for presence/replace check
	attrValueCache  sync.Map // attr → *regexp.Regexp for value capture
)

// cachedAttrSearchRE returns (caching) a regex that matches `attr="value"` for replacement.
func cachedAttrSearchRE(attr string) *regexp.Regexp {
	if v, ok := attrSearchCache.Load(attr); ok {
		if re, ok2 := v.(*regexp.Regexp); ok2 {
			return re
		}
	}
	re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(attr) + `\s*=\s*["'][^"']*["']`)
	attrSearchCache.Store(attr, re)
	return re
}

// cachedAttrValueRE returns (caching) a regex that captures the value of `attr="..."`.
func cachedAttrValueRE(attr string) *regexp.Regexp {
	if v, ok := attrValueCache.Load(attr); ok {
		if re, ok2 := v.(*regexp.Regexp); ok2 {
			return re
		}
	}
	re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(attr) + `\s*=\s*["']([^"']+)["']`)
	attrValueCache.Store(attr, re)
	return re
}

// replaceTagWith applies replacer to every match of re in doc, rebuilding the
// document with a strings.Builder. refs contains one string per capture group.
func replaceTagWith(doc string, re *regexp.Regexp, replacer func(tag string, refs []string) (string, error)) (string, error) {
	matches := re.FindAllStringSubmatchIndex(doc, -1)
	if len(matches) == 0 {
		return doc, nil
	}

	var out strings.Builder
	last := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		out.WriteString(doc[last:start])
		tag := doc[start:end]

		refs := make([]string, len(m)/2)
		for i := 0; i < len(m); i += 2 {
			if m[i] >= 0 && m[i+1] >= 0 {
				refs[i/2] = doc[m[i]:m[i+1]]
			}
		}

		replacement, err := replacer(tag, refs)
		if err != nil {
			return "", err
		}
		out.WriteString(replacement)
		last = end
	}
	out.WriteString(doc[last:])
	return out.String(), nil
}

// addOrReplaceAttr sets an HTML attribute value in a tag, replacing it if present.
func addOrReplaceAttr(tag, attr, value string) string {
	re := cachedAttrSearchRE(attr)
	quoted := attr + `="` + value + `"`
	if re.MatchString(tag) {
		return re.ReplaceAllString(tag, quoted)
	}
	// Insert before the closing > of the tag.
	if idx := strings.LastIndex(tag, ">"); idx != -1 {
		return tag[:idx] + " " + quoted + tag[idx:]
	}
	return tag
}

// getTagAttr extracts the value of a named attribute from an HTML tag string.
func getTagAttr(tag, attr string) string {
	m := cachedAttrValueRE(attr).FindStringSubmatch(tag)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func htmlEscape(v string) string {
	v = strings.ReplaceAll(v, "&", "&amp;")
	v = strings.ReplaceAll(v, "\"", "&quot;")
	v = strings.ReplaceAll(v, "<", "&lt;")
	v = strings.ReplaceAll(v, ">", "&gt;")
	return v
}

func encodeBase64(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

// isExternalRef reports whether ref is an absolute external URL (http/https/scheme-relative).
func isExternalRef(ref string) bool {
	lower := strings.ToLower(strings.TrimSpace(ref))
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "//")
}
