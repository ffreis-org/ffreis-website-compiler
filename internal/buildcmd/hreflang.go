package buildcmd

import (
	"fmt"
	"regexp"
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
//	base_url + variant.path + "/" + slug + "/"  (clean URLs)
//	base_url + variant.path + "/" + slug + ".html"  (non-clean URLs)
//
// Where slug is looked up from lang_links[].slug_map for that hreflang (falls
// back to pageName). For the index page the page segment is omitted.
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

	slugMaps := buildLangLinksSlugMap(siteData)
	htmlLang, _ := siteData["html_lang"].(string)
	currentSlug := resolvePageSlug(siteData, pageName)

	var links strings.Builder
	for _, v := range variants {
		slug := variantSlug(v.hreflang, htmlLang, currentSlug, slugMaps, pageName)
		fmt.Fprintf(&links, `<link rel="alternate" hreflang="%s" href="%s">`,
			v.hreflang, buildAlternateURL(baseURL, v.path, slug, cleanURLs))
		links.WriteByte('\n')
	}
	if defaultHreflang != "" {
		appendXDefaultLink(xDefaultLinkContext{
			sb:              &links,
			variants:        variants,
			defaultHreflang: defaultHreflang,
			htmlLang:        htmlLang,
			currentSlug:     currentSlug,
			slugMaps:        slugMaps,
			pageName:        pageName,
			baseURL:         baseURL,
			cleanURLs:       cleanURLs,
		})
	}

	return strings.Replace(html, "</head>", links.String()+"</head>", 1)
}

// variantSlug returns the page slug for the given hreflang. When the variant
// is the current page language it returns the already-resolved currentSlug;
// otherwise it looks up the slug in the slug maps.
func variantSlug(hreflang, htmlLang, currentSlug string, slugMaps map[string]map[string]string, pageName string) string {
	if hreflang == htmlLang {
		return currentSlug
	}
	return resolveSlugForLang(slugMaps, hreflang, pageName)
}

// xDefaultLinkContext carries the context needed to emit an x-default hreflang
// link, replacing a parameter list that would otherwise exceed the max allowed.
type xDefaultLinkContext struct {
	sb              *strings.Builder
	variants        []langVariant
	defaultHreflang string
	htmlLang        string
	currentSlug     string
	slugMaps        map[string]map[string]string
	pageName        string
	baseURL         string
	cleanURLs       bool
}

// appendXDefaultLink writes an x-default hreflang link to ctx.sb for the
// default language variant, if one exists in ctx.variants.
func appendXDefaultLink(ctx xDefaultLinkContext) {
	for _, v := range ctx.variants {
		if v.hreflang != ctx.defaultHreflang {
			continue
		}
		slug := variantSlug(v.hreflang, ctx.htmlLang, ctx.currentSlug, ctx.slugMaps, ctx.pageName)
		fmt.Fprintf(ctx.sb, `<link rel="alternate" hreflang="x-default" href="%s">`,
			buildAlternateURL(ctx.baseURL, v.path, slug, ctx.cleanURLs))
		ctx.sb.WriteByte('\n')
		break
	}
}

// injectLangSwitcherHrefs replaces the static root href on each non-active
// lang-flag anchor with a per-page href pointing to the parallel page in the
// sibling language. It reads slug_map from each lang_links entry to translate
// the page key to the sibling language's slug.
func injectLangSwitcherHrefs(html string, siteData map[string]any, pageName string, cleanURLs bool) string {
	ui, _ := siteData["ui"].(map[string]any)
	nav, _ := ui["nav"].(map[string]any)
	langLinks, _ := nav["lang_links"].([]any)
	for _, item := range langLinks {
		link, _ := item.(map[string]any)
		if active, _ := link["active"].(bool); active {
			continue
		}
		oldHref, _ := link["href"].(string)
		if oldHref == "" {
			continue
		}
		siblingSlug := pageName
		if slugMap, _ := link["slug_map"].(map[string]any); slugMap != nil {
			if s, _ := slugMap[pageName].(string); s != "" {
				siblingSlug = s
			}
		}
		base := strings.TrimRight(oldHref, "/")
		var newHref string
		if pageName == "index" {
			newHref = base + "/"
		} else if cleanURLs {
			newHref = base + "/" + siblingSlug + "/"
		} else {
			newHref = base + "/" + siblingSlug + ".html"
		}
		re := regexp.MustCompile(`class="lang-flag" href="` + regexp.QuoteMeta(oldHref) + `"`)
		html = re.ReplaceAllString(html, `class="lang-flag" href="`+newHref+`"`)
	}
	return html
}

func buildAlternateURL(baseURL, langPath, slug string, cleanURLs bool) string {
	if slug == "index" {
		return baseURL + langPath + "/"
	}
	if cleanURLs {
		return baseURL + langPath + "/" + slug + "/"
	}
	return baseURL + langPath + "/" + slug + ".html"
}

// buildLangLinksSlugMap builds a lookup table hreflang → (pageName → slug)
// from the ui.nav.lang_links entries in site data.
func buildLangLinksSlugMap(siteData map[string]any) map[string]map[string]string {
	ui, _ := siteData["ui"].(map[string]any)
	nav, _ := ui["nav"].(map[string]any)
	langLinks, _ := nav["lang_links"].([]any)
	result := make(map[string]map[string]string)
	for _, item := range langLinks {
		link, _ := item.(map[string]any)
		lang, _ := link["lang"].(string)
		if lang == "" {
			continue
		}
		slugMapRaw, _ := link["slug_map"].(map[string]any)
		if len(slugMapRaw) == 0 {
			continue
		}
		m := make(map[string]string, len(slugMapRaw))
		for k, v := range slugMapRaw {
			if s, _ := v.(string); s != "" {
				m[k] = s
			}
		}
		result[lang] = m
	}
	return result
}

// resolveSlugForLang looks up the slug for pageName in the given hreflang's
// slug_map, falling back to pageName when absent.
func resolveSlugForLang(slugMaps map[string]map[string]string, hreflang, pageName string) string {
	if m, ok := slugMaps[hreflang]; ok {
		if s, ok := m[pageName]; ok && s != "" {
			return s
		}
	}
	return pageName
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

	// language_default is a URL prefix code ("en", "pt") — convert to hreflang
	// after building the variants list so we can match by path prefix.
	defaultPrefix, _ := siteData["language_default"].(string)

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

	// Find the hreflang code for the default language by matching URL prefix.
	defaultHreflang := ""
	for _, v := range variants {
		if strings.TrimPrefix(v.path, "/") == defaultPrefix {
			defaultHreflang = v.hreflang
			break
		}
	}

	return variants, defaultHreflang
}
