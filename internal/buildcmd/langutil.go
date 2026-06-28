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

// ValidatePostLangs returns an error if any post declares an available_languages
// code that is not configured in the site's language_variants. A post declaring
// an unsupported language would produce an unreachable route in the built site.
func ValidatePostLangs(logger *slog.Logger, postList []posts.Post, siteData map[string]any) error {
	known := knownLangPrefixes(siteData)
	if len(known) == 0 {
		return nil
	}
	var unknown []string
	for _, p := range postList {
		for _, lang := range p.Meta.AvailableLanguages {
			if !slices.Contains(known, lang) {
				logger.Error("post available_languages contains unsupported language code",
					"slug", p.Meta.Slug,
					"unsupported_lang", lang,
					"supported_langs", known,
				)
				unknown = append(unknown, fmt.Sprintf("%s:%s", p.Meta.Slug, lang))
			}
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("posts reference unsupported language codes (add them to language_variants or remove from posts): %v", unknown)
	}
	return nil
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
