package posts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePost creates a posts/<slug>/index.md fixture with the given content.
func writePost(t *testing.T, root, slug, content string) {
	t.Helper()
	dir := filepath.Join(root, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write index.md: %v", err)
	}
}

const validPostFrontmatter = `---
title: "Hello World"
date: "2026-03-01"
summary: "A short summary."
canonical_url: "https://example.com/hello"
tags: ["go", "blog"]
---
# Hello

Paragraph with ![alt](./images/diagram.webp) image.
`

// TestLoadPostsDir_HappyPath pins the loader behaviour for a well-formed post:
// frontmatter parsing, markdown rendering, image-path rewriting, slug
// inference. Catches regressions in any of those layers.
func TestLoadPostsDirHappyPath(t *testing.T) {
	root := t.TempDir()
	writePost(t, root, "hello-world", validPostFrontmatter)

	posts, err := LoadPostsDir(root)
	if err != nil {
		t.Fatalf("LoadPostsDir: %v", err)
	}
	if len(posts) != 1 {
		t.Fatalf("got %d posts, want 1", len(posts))
	}

	p := posts[0]
	if p.Meta.Slug != "hello-world" {
		t.Errorf("Slug = %q, want hello-world", p.Meta.Slug)
	}
	if p.Meta.Title != "Hello World" {
		t.Errorf("Title = %q, want %q", p.Meta.Title, "Hello World")
	}
	if p.Meta.Date != "2026-03-01" {
		t.Errorf("Date = %q, want 2026-03-01", p.Meta.Date)
	}
	if p.Meta.CanonicalURL != "https://example.com/hello" {
		t.Errorf("CanonicalURL = %q", p.Meta.CanonicalURL)
	}
	if len(p.Meta.Tags) != 2 || p.Meta.Tags[0] != "go" || p.Meta.Tags[1] != "blog" {
		t.Errorf("Tags = %v, want [go blog]", p.Meta.Tags)
	}

	// Image rewrite: ./images/diagram.webp -> /blog/hello-world/images/diagram.webp
	if !strings.Contains(p.BodyHTML, `src="/blog/hello-world/images/diagram.webp"`) {
		t.Errorf("image path not rewritten:\n%s", p.BodyHTML)
	}
	// Heading rendered.
	if !strings.Contains(p.BodyHTML, "<h1") || !strings.Contains(p.BodyHTML, "Hello</h1>") {
		t.Errorf("heading not rendered:\n%s", p.BodyHTML)
	}
}

// TestLoadPostsDir_SortOrder pins the contract: newest date first, then by
// slug. Both the blog index and the RSS feed depend on this ordering.
func TestLoadPostsDirSortOrder(t *testing.T) {
	root := t.TempDir()
	writePost(t, root, "older-post", `---
title: "Older"
date: "2026-01-01"
---
older
`)
	writePost(t, root, "newer-post", `---
title: "Newer"
date: "2026-03-01"
---
newer
`)
	writePost(t, root, "another-newer", `---
title: "Another"
date: "2026-03-01"
---
another
`)

	posts, err := LoadPostsDir(root)
	if err != nil {
		t.Fatalf("LoadPostsDir: %v", err)
	}
	want := []string{"another-newer", "newer-post", "older-post"}
	for i, p := range posts {
		if p.Meta.Slug != want[i] {
			t.Errorf("post %d: slug = %q, want %q", i, p.Meta.Slug, want[i])
		}
	}
}

// TestLoadPostsDir_MissingRequiredFields verifies that a post without title
// or date is rejected with a useful error rather than silently dropped.
func TestLoadPostsDirMissingRequiredFields(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantSub string
	}{
		{
			"missing title",
			`---
date: "2026-01-01"
---
body`,
			"title",
		},
		{
			"missing date",
			`---
title: "T"
---
body`,
			"date",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writePost(t, root, "p", tc.content)
			_, err := LoadPostsDir(root)
			if err == nil {
				t.Fatalf("expected error mentioning %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err = %v, want substring %q", err, tc.wantSub)
			}
		})
	}
}

// TestLoadPostsDir_SlugMismatchRejected verifies that a frontmatter slug
// disagreeing with the directory name fails fast, rather than producing a
// post whose URL doesn't match its file path.
func TestLoadPostsDirSlugMismatchRejected(t *testing.T) {
	root := t.TempDir()
	writePost(t, root, "real-slug", `---
title: "T"
date: "2026-01-01"
slug: "different-slug"
---
body
`)
	_, err := LoadPostsDir(root)
	if err == nil || !strings.Contains(err.Error(), "slug") {
		t.Fatalf("expected slug mismatch error, got %v", err)
	}
}

// TestLoadPostsDir_SkipsNonPostDirs verifies that directories without an
// index.md and non-directory entries don't break loading.
func TestLoadPostsDirSkipsNonPostDirs(t *testing.T) {
	root := t.TempDir()
	writePost(t, root, "real", validPostFrontmatter)

	// Loose file at root (should be skipped, not treated as a post).
	if err := os.WriteFile(filepath.Join(root, "stray.txt"), []byte("not a post"), 0o644); err != nil {
		t.Fatalf("write stray.txt: %v", err)
	}
	// Directory with no index.md (should be skipped, not error).
	if err := os.MkdirAll(filepath.Join(root, "no-index"), 0o755); err != nil {
		t.Fatalf("mkdir no-index: %v", err)
	}

	posts, err := LoadPostsDir(root)
	if err != nil {
		t.Fatalf("LoadPostsDir: %v", err)
	}
	if len(posts) != 1 || posts[0].Meta.Slug != "real" {
		t.Errorf("got %d posts %v, want only 'real'", len(posts), posts)
	}
}

// TestLoadPostsDir_ThumbnailRewriting verifies the ./images/foo -> /blog/slug/foo
// expansion for the thumbnail frontmatter field — this URL is what shows up
// in social-preview cards.
func TestLoadPostsDirThumbnailRewriting(t *testing.T) {
	root := t.TempDir()
	writePost(t, root, "with-thumb", `---
title: "T"
date: "2026-01-01"
thumbnail: "./images/cover.webp"
---
body
`)
	posts, err := LoadPostsDir(root)
	if err != nil {
		t.Fatalf("LoadPostsDir: %v", err)
	}
	want := "/blog/with-thumb/images/cover.webp"
	if posts[0].Meta.Thumbnail != want {
		t.Errorf("Thumbnail = %q, want %q", posts[0].Meta.Thumbnail, want)
	}
}
