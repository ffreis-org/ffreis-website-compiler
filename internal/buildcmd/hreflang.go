package buildcmd

import (
	"fmt"
	"strings"
)

type langVariant struct {
	hreflang string
	path     string
}

// injectHreflangAlternates inserts <link rel="alternate" hreflang="..."> tags
// into the page's <head> for every entry in language_variants from site data.
//
// It also emits an x-default link pointing to the language_default variant.
// If language_variants is absent or empty, the page is returned unchanged.
//
// The alternate URLs are constructed as:
//
//	base_url + variant.path + "/" + pageName + "/"  (clean URLs)
//	base_url + variant.path + "/" + pageName + ".html"  (non-clean URLs)
//
// For the index page the page segment is omitted, giving base_url + variant.path + "/".
func injectHreflangAlternates(html string, siteData map[string]any, pageName string, cleanURLs bool) string {
	baseURL, _ := siteData["base_url"].(string)
	if baseURL == "" {
		return html
	}
	baseURL = strings.TrimRight(baseURL, "/")

	variants, defaultHreflang := extractLangVariants(siteData)
	if len(variants) == 0 {
		return html
	}

	var links strings.Builder
	for _, v := range variants {
		fmt.Fprintf(&links, `<link rel="alternate" hreflang="%s" href="%s">`,
			v.hreflang, buildAlternateURL(baseURL, v.path, pageName, cleanURLs))
		links.WriteByte('\n')
	}
	if defaultHreflang != "" {
		for _, v := range variants {
			if v.hreflang == defaultHreflang {
				fmt.Fprintf(&links, `<link rel="alternate" hreflang="x-default" href="%s">`,
					buildAlternateURL(baseURL, v.path, pageName, cleanURLs))
				links.WriteByte('\n')
				break
			}
		}
	}

	return strings.Replace(html, "</head>", links.String()+"</head>", 1)
}

func buildAlternateURL(baseURL, langPath, pageName string, cleanURLs bool) string {
	if pageName == "index" {
		return baseURL + langPath + "/"
	}
	if cleanURLs {
		return baseURL + langPath + "/" + pageName + "/"
	}
	return baseURL + langPath + "/" + pageName + ".html"
}

func extractLangVariants(siteData map[string]any) ([]langVariant, string) {
	raw, ok := siteData["language_variants"]
	if !ok {
		return nil, ""
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, ""
	}

	defaultHreflang, _ := siteData["language_default"].(string)

	var variants []langVariant
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		hreflang, _ := m["hreflang"].(string)
		path, _ := m["path"].(string)
		if hreflang == "" || path == "" {
			continue
		}
		variants = append(variants, langVariant{hreflang: hreflang, path: path})
	}
	return variants, defaultHreflang
}
