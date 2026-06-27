package buildcmd

import (
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
// Returns "" if available is empty (should not happen when called after isAvailable).
func redirectTarget(currentLang string, available []string, siteData map[string]any) string {
	siteDefault, _ := siteData["language_default"].(string)

	candidates := []string{siteDefault, "en"}
	for _, c := range candidates {
		if c != "" && c != currentLang && isAvailable(available, c) {
			return c
		}
	}
	for _, l := range available {
		if l != currentLang {
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

// WarnUnknownLangs logs a warning for any post whose available_languages
// contains a code not found in the site's configured language_variants.
// This catches typos like "br" when the deployment uses "pt".
func WarnUnknownLangs(logger *slog.Logger, postList []posts.Post, siteData map[string]any) {
	known := knownLangPrefixes(siteData)
	if len(known) == 0 {
		return
	}
	for _, p := range postList {
		for _, lang := range p.Meta.AvailableLanguages {
			if !slices.Contains(known, lang) {
				logger.Warn("post available_languages contains unknown language code",
					"slug", p.Meta.Slug,
					"unknown_lang", lang,
					"known_langs", known,
				)
			}
		}
	}
}

// knownLangPrefixes extracts the URL prefix codes (e.g. "en", "pt") from
// language_variants in siteData.
func knownLangPrefixes(siteData map[string]any) []string {
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
