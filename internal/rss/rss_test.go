package rss

import (
	"encoding/xml"
	"strings"
	"testing"

	"ffreis-website-compiler/internal/posts"
)

func mustPost(title, date, slug, canonical, summary string) posts.Post {
	return posts.Post{
		Meta: posts.PostMeta{
			Title:        title,
			Date:         date,
			Slug:         slug,
			Summary:      summary,
			CanonicalURL: canonical,
		},
	}
}

// TestGenerateRSS_HappyPath verifies the structural contract of the feed:
// XML is well-formed, channel metadata matches the FeedConfig, and each
// post becomes one <item> with the expected link, GUID, title, description,
// and a parseable pubDate. Substack imports require all of these.
func TestGenerateRSSHappyPath(t *testing.T) {
	cfg := FeedConfig{
		Title:       "Example Blog",
		Link:        "https://ffreis.com",
		Description: "Personal writing.",
		Language:    "en",
	}
	in := []posts.Post{
		mustPost("Newest", "2026-03-15", "newest", "https://example.com/canon-newest", "Newest summary"),
		mustPost("Older", "2026-01-10", "older-slug", "", "Older summary"),
	}

	raw, err := GenerateRSS(cfg, in)
	if err != nil {
		t.Fatalf("GenerateRSS: %v", err)
	}

	// Must be a parseable XML document with the rss element at the root.
	var parsed rssRoot
	if err := xml.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("output not valid XML: %v\n%s", err, raw)
	}
	if parsed.Version != "2.0" {
		t.Errorf("rss version = %q, want 2.0", parsed.Version)
	}
	if parsed.Channel.Title != "Example Blog" || parsed.Channel.Link != "https://ffreis.com" {
		t.Errorf("channel metadata mismatch: %+v", parsed.Channel)
	}
	if parsed.Channel.Language != "en" {
		t.Errorf("language = %q, want en", parsed.Channel.Language)
	}
	if len(parsed.Channel.Items) != 2 {
		t.Fatalf("got %d items, want 2", len(parsed.Channel.Items))
	}

	// First item uses its explicit canonical URL.
	it0 := parsed.Channel.Items[0]
	if it0.Title != "Newest" {
		t.Errorf("item 0 Title = %q", it0.Title)
	}
	if it0.Link != "https://example.com/canon-newest" {
		t.Errorf("item 0 Link = %q, want canonical URL", it0.Link)
	}
	if it0.GUID.Value != it0.Link || it0.GUID.IsPermaLink != "true" {
		t.Errorf("item 0 GUID = %+v, want PermaLink with value=Link", it0.GUID)
	}
	if it0.Description != "Newest summary" {
		t.Errorf("item 0 Description = %q", it0.Description)
	}
	if it0.PubDate == "" {
		t.Error("item 0 PubDate is empty")
	}

	// Second item has no canonical URL — must fall back to cfg.Link + /blog/slug/.
	it1 := parsed.Channel.Items[1]
	wantLink := "https://ffreis.com/blog/older-slug/"
	if it1.Link != wantLink {
		t.Errorf("item 1 Link = %q, want %q (canonical-URL fallback)", it1.Link, wantLink)
	}
}

// TestGenerateRSS_EmptyList still produces a valid RSS document with zero items.
func TestGenerateRSSEmptyList(t *testing.T) {
	cfg := FeedConfig{Title: "T", Link: "https://e.com", Description: "d", Language: "en"}
	raw, err := GenerateRSS(cfg, nil)
	if err != nil {
		t.Fatalf("GenerateRSS empty: %v", err)
	}

	var parsed rssRoot
	if err := xml.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("empty feed not valid XML: %v", err)
	}
	if len(parsed.Channel.Items) != 0 {
		t.Errorf("expected 0 items, got %d", len(parsed.Channel.Items))
	}
}

// TestGenerateRSS_RejectsInvalidDate ensures a malformed `date` frontmatter
// surfaces an error instead of silently producing an item with an empty
// PubDate (which Substack and feed readers misorder or drop).
func TestGenerateRSSRejectsInvalidDate(t *testing.T) {
	cfg := FeedConfig{Title: "T", Link: "https://e.com"}
	in := []posts.Post{mustPost("Bad", "March 1st, 2026", "bad-date", "", "")}
	_, err := GenerateRSS(cfg, in)
	if err == nil {
		t.Fatal("expected date-parse error, got nil")
	}
	if !strings.Contains(err.Error(), "date") {
		t.Errorf("err = %v, want substring 'date'", err)
	}
}

// TestGenerateRSS_OutputStartsWithXMLProlog confirms the XML header is
// prepended; readers reject feeds that lack it.
func TestGenerateRSSOutputStartsWithXMLProlog(t *testing.T) {
	cfg := FeedConfig{Title: "T", Link: "https://e.com"}
	raw, err := GenerateRSS(cfg, nil)
	if err != nil {
		t.Fatalf("GenerateRSS: %v", err)
	}
	if !strings.HasPrefix(string(raw), `<?xml version="1.0" encoding="UTF-8"?>`) {
		t.Errorf("output missing XML prolog: %q", string(raw[:60]))
	}
}
