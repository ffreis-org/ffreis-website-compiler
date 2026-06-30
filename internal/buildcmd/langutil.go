package buildcmd

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"ffreis-website-compiler/internal/posts"
)

// currentLangPrefix returns the URL prefix code ("en", "pt", "jp") for the
// current build by stripping the leading "/" from base_path in site data.
// Returns "" if base_path is absent or not a string.
func currentLangPrefix(siteData map[string]any) string {
	bp, _ := siteData["base_path"].(string)
	return strings.TrimPrefix(bp, "/")
}

// isAvailable reports whether lang is considered available for a content item.
// An empty/nil available list means the item is available in all languages.
func isAvailable(available []string, lang string) bool {
	if len(available) == 0 {
		return true
	}
	for _, l := range available {
		if l == lang {
			return true
		}
	}
	return false
}

// redirectTarget returns the URL prefix to redirect to when content is not
// available in currentLang. Priority: site default → "en" → first available.
// Only *rendered* languages (those the site actually builds pages for) are
// eligible targets — an item available only in content-only languages (e.g.
// "de", "ja") has no rendered route to redirect to, so this returns "" and the
// caller falls back to the language home. Returns "" if available is empty
// (should not happen when called after isAvailable).
func redirectTarget(currentLang string, available []string, siteData map[string]any) string {
	rendered := renderedLangPrefixes(siteData)
	siteDefault, _ := siteData["language_default"].(string)

	candidates := []string{siteDefault, "en"}
	for _, c := range candidates {
		if c != "" && c != currentLang && slices.Contains(rendered, c) && isAvailable(available, c) {
			return c
		}
	}
	for _, l := range available {
		if l != currentLang && slices.Contains(rendered, l) {
			return l
		}
	}
	return ""
}

// toLangsAny converts a []string to []any for template consumption.
func toLangsAny(langs []string) []any {
	if len(langs) == 0 {
		return nil
	}
	out := make([]any, len(langs))
	for i, l := range langs {
		out[i] = l
	}
	return out
}

// ValidatePostLangs returns an error if any post declares an available_languages
// code that is not part of the site's content-language set (the rendered
// languages plus any extra content_languages it carries, e.g. "de"/"ja"). A
// code outside that set — a typo like "br" instead of "pt" — would produce an
// unreachable route in the built site, so it is caught at build time. Codes that
// are content-only (carried but not rendered) are allowed: the item simply has
// no rendered page and falls back via the redirect-stub path.
func ValidatePostLangs(logger *slog.Logger, postList []posts.Post, siteData map[string]any) error {
	allowed := allowedContentLangs(siteData)
	if len(allowed) == 0 {
		return nil
	}
	var unknown []string
	for _, p := range postList {
		for _, lang := range p.Meta.AvailableLanguages {
			if !slices.Contains(allowed, lang) {
				logger.Error("post available_languages contains unsupported language code",
					"slug", p.Meta.Slug,
					"unsupported_lang", lang,
					"allowed_langs", allowed,
				)
				unknown = append(unknown, fmt.Sprintf("%s:%s", p.Meta.Slug, lang))
			}
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("posts reference unsupported language codes (add them to content_languages/language_variants or fix the typo): %v", unknown)
	}
	return nil
}

// renderedLangPrefixes extracts the URL prefix codes the site actually builds
// pages for (e.g. "en", "pt"), from language_variants in siteData.
func renderedLangPrefixes(siteData map[string]any) []string {
	variants, _ := siteData["language_variants"].([]any)
	out := make([]string, 0, len(variants))
	for _, v := range variants {
		vm, _ := v.(map[string]any)
		path, _ := vm["path"].(string)
		prefix := strings.TrimPrefix(path, "/")
		if prefix != "" {
			out = append(out, prefix)
		}
	}
	return out
}

// allowedContentLangs returns the set of language codes a content item may
// legitimately declare in available_languages: the content-language superset of
// the rendered languages plus any extra content_languages the site carries but
// does not itself render (e.g. "de", "ja"). Falls back to the rendered set when
// content_languages is absent, preserving the stricter prior behaviour.
func allowedContentLangs(siteData map[string]any) []string {
	allowed := renderedLangPrefixes(siteData)
	extra, _ := siteData["content_languages"].([]any)
	for _, e := range extra {
		code, ok := e.(string)
		if ok && code != "" && !slices.Contains(allowed, code) {
			allowed = append(allowed, code)
		}
	}
	return allowed
}
