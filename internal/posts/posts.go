package posts

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	meta "github.com/yuin/goldmark-meta"
	"github.com/yuin/goldmark/parser"
)

// PostMeta holds the frontmatter fields parsed from a post's index.md.
type PostMeta struct {
	Title           string
	Date            string
	Slug            string
	Summary         string
	Thumbnail       string // root-relative path: /blog/slug/images/thumb.webp (empty if none)
	Tags            []string
	CanonicalURL    string
	MediumPublished bool
}

// Post holds the parsed content of a single blog post.
type Post struct {
	Meta     PostMeta
	BodyHTML string // goldmark-rendered HTML, safe to pass to safeHTML template function
	Dir      string // absolute path to the post directory (for image copying)
}

var imgSrcRE = regexp.MustCompile(`(<img\b[^>]*?\bsrc=")(\./images/[^"]+)(")`)

// LoadPostsDir walks postsDir (the posts/ subdirectory of ffreis-posts),
// parses each posts/<slug>/index.md, renders body to HTML, and returns
// posts sorted newest-first by date then slug.
func LoadPostsDir(postsDir string) ([]Post, error) {
	entries, err := os.ReadDir(postsDir)
	if err != nil {
		return nil, fmt.Errorf("reading posts directory %s: %w", postsDir, err)
	}

	md := goldmark.New(
		goldmark.WithExtensions(meta.Meta),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	)

	var result []Post
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		slug := entry.Name()
		postDir := filepath.Join(postsDir, slug)
		indexPath := filepath.Join(postDir, "index.md")

		if _, err := os.Stat(indexPath); err != nil {
			continue
		}

		raw, err := os.ReadFile(indexPath)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", indexPath, err)
		}

		context := parser.NewContext()
		var bodyBuf bytes.Buffer
		if err := md.Convert(raw, &bodyBuf, parser.WithContext(context)); err != nil {
			return nil, fmt.Errorf("rendering markdown for %s: %w", slug, err)
		}

		fm := meta.Get(context)
		postMeta, err := parsePostMeta(fm, slug, postDir)
		if err != nil {
			return nil, fmt.Errorf("parsing frontmatter for %s: %w", slug, err)
		}

		bodyHTML := rewriteImagePaths(bodyBuf.String(), slug)

		result = append(result, Post{
			Meta:     postMeta,
			BodyHTML: bodyHTML,
			Dir:      postDir,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Meta.Date != result[j].Meta.Date {
			return result[i].Meta.Date > result[j].Meta.Date
		}
		return result[i].Meta.Slug < result[j].Meta.Slug
	})

	return result, nil
}

func parsePostMeta(fm map[string]any, slug, postDir string) (PostMeta, error) {
	m := PostMeta{Slug: slug}

	if v, ok := fm["title"].(string); ok {
		m.Title = v
	}
	if m.Title == "" {
		return PostMeta{}, fmt.Errorf("missing required frontmatter field 'title'")
	}

	if v, ok := fm["date"].(string); ok {
		m.Date = v
	}
	if m.Date == "" {
		return PostMeta{}, fmt.Errorf("missing required frontmatter field 'date'")
	}

	if v, ok := fm["summary"].(string); ok {
		m.Summary = v
	}
	if v, ok := fm["canonical_url"].(string); ok {
		m.CanonicalURL = v
	}
	if v, ok := fm["medium_published"].(bool); ok {
		m.MediumPublished = v
	}

	// Validate slug matches directory name
	if slugField, ok := fm["slug"].(string); ok && slugField != "" && slugField != slug {
		return PostMeta{}, fmt.Errorf("frontmatter slug %q does not match directory name %q", slugField, slug)
	}

	// Resolve thumbnail to root-relative path
	if thumbRel, ok := fm["thumbnail"].(string); ok && thumbRel != "" {
		// Convert ./images/foo.webp → /blog/slug/images/foo.webp
		clean := strings.TrimPrefix(thumbRel, "./")
		m.Thumbnail = "/blog/" + slug + "/" + clean
	}

	if tags, ok := fm["tags"].([]any); ok {
		for _, t := range tags {
			if s, ok := t.(string); ok {
				m.Tags = append(m.Tags, s)
			}
		}
	}

	return m, nil
}

// rewriteImagePaths rewrites ./images/foo.webp src attributes in the rendered HTML
// to root-relative paths /blog/<slug>/images/foo.webp.
func rewriteImagePaths(html, slug string) string {
	return imgSrcRE.ReplaceAllStringFunc(html, func(match string) string {
		parts := imgSrcRE.FindStringSubmatch(match)
		if len(parts) != 4 {
			return match
		}
		rel := strings.TrimPrefix(parts[2], "./")
		return parts[1] + "/blog/" + slug + "/" + rel + parts[3]
	})
}

// CopyPostImages copies each post's images/ subdirectory into
// outDir/blog/<slug>/images/ so they are served from the website.
func CopyPostImages(postList []Post, outDir string) error {
	for _, post := range postList {
		imagesDir := filepath.Join(post.Dir, "images")
		if _, err := os.Stat(imagesDir); err != nil {
			continue // no images dir, skip
		}

		destDir := filepath.Join(outDir, "blog", post.Meta.Slug, "images")
		if err := copyDir(imagesDir, destDir); err != nil {
			return fmt.Errorf("copying images for post %s: %w", post.Meta.Slug, err)
		}
	}
	return nil
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644) //nolint:gosec
}
