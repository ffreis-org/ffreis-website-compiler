package buildcmd

import (
	"io"
	"log/slog"
	"testing"

	"ffreis-website-compiler/internal/posts"
)

// siteWith builds a minimal siteData map with the given rendered language
// prefixes and optional content-only languages.
func siteWith(rendered []string, content []string) map[string]any {
	variants := make([]any, len(rendered))
	for i, r := range rendered {
		variants[i] = map[string]any{"path": "/" + r}
	}
	site := map[string]any{
		"language_variants": variants,
		"language_default":  "en",
	}
	if content != nil {
		extra := make([]any, len(content))
		for i, c := range content {
			extra[i] = c
		}
		site["content_languages"] = extra
	}
	return site
}

func postWith(slug string, langs ...string) posts.Post {
	return posts.Post{Meta: posts.PostMeta{Slug: slug, AvailableLanguages: langs}}
}

func TestAllowedContentLangs_SupersetOfRenderedPlusContent(t *testing.T) {
	got := allowedContentLangs(siteWith([]string{"en", "pt"}, []string{"de", "ja", "pt"}))
	want := map[string]bool{"en": true, "pt": true, "de": true, "ja": true}
	if len(got) != len(want) {
		t.Fatalf("expected %d unique langs, got %v", len(want), got)
	}
	for _, l := range got {
		if !want[l] {
			t.Errorf("unexpected lang %q in allowed set %v", l, got)
		}
	}
}

func TestAllowedContentLangs_FallsBackToRenderedWhenNoContentLangs(t *testing.T) {
	got := allowedContentLangs(siteWith([]string{"en", "pt"}, nil))
	if len(got) != 2 {
		t.Fatalf("expected rendered-only fallback [en pt], got %v", got)
	}
}

func TestValidatePostLangs_AllowsContentOnlyLanguages(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	site := siteWith([]string{"en", "pt"}, []string{"de", "ja"})
	postList := []posts.Post{
		postWith("rendered-pair", "en", "pt"),
		postWith("content-only", "de", "ja"),
		postWith("mixed", "en", "de"),
		postWith("all-langs"), // nil = all
	}
	if err := ValidatePostLangs(logger, postList, site); err != nil {
		t.Fatalf("expected de/ja content languages to be allowed, got error: %v", err)
	}
}

func TestValidatePostLangs_RejectsTypoOutsideContentSet(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	site := siteWith([]string{"en", "pt"}, []string{"de", "ja"})
	// "br" is a common typo for "pt"; it is not in the content set → error.
	postList := []posts.Post{postWith("typo", "en", "br")}
	err := ValidatePostLangs(logger, postList, site)
	if err == nil {
		t.Fatal("expected an error for unsupported language code 'br', got nil")
	}
}

func TestRedirectTarget_OnlyTargetsRenderedLanguages(t *testing.T) {
	site := siteWith([]string{"en", "pt"}, []string{"de", "ja"})

	// A de/ja-only item viewed in the en build has no rendered route to redirect
	// to → "" so the caller falls back to the language home (never /de/).
	if got := redirectTarget("en", []string{"de", "ja"}, site); got != "" {
		t.Errorf("content-only item should have no rendered redirect target, got %q", got)
	}

	// An en+de item viewed in the pt build redirects to en (rendered), not de.
	if got := redirectTarget("pt", []string{"en", "de"}, site); got != "en" {
		t.Errorf("expected redirect to rendered 'en', got %q", got)
	}
}
