package buildcmd

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"ffreis-website-compiler/internal/posts"
	"ffreis-website-compiler/internal/rss"
	"ffreis-website-compiler/internal/sitegen"
	"ffreis-website-compiler/internal/sitemap"
)

// postToMap converts a Post to the map[string]any shape expected by templates.
func postToMap(p posts.Post) map[string]any {
	tagsAny := make([]any, len(p.Meta.Tags))
	for i, t := range p.Meta.Tags {
		tagsAny[i] = t
	}
	return map[string]any{
		"title":     p.Meta.Title,
		"date":      p.Meta.Date,
		"summary":   p.Meta.Summary,
		"href":      "/blog/" + p.Meta.Slug + "/",
		"thumbnail": p.Meta.Thumbnail,
		"tags":      tagsAny,
	}
}

// injectPostsBlogList replaces pages.blog.posts in siteData with metadata
// derived from the loaded posts. previewN controls how many items are stored
// in posts (used by the home-page carousel); pass 0 to store all.
// Called after contract validation so the injected data does not need
// contract entries.
func injectPostsBlogList(siteData map[string]any, postList []posts.Post, previewN int) {
	pagesData, _ := siteData["pages"].(map[string]any)
	if pagesData == nil {
		return
	}
	blogData, _ := pagesData["blog"].(map[string]any)
	if blogData == nil {
		return
	}

	items := make([]any, 0, len(postList))
	for _, p := range postList {
		items = append(items, postToMap(p))
	}

	// posts key keeps the preview slice for the home carousel.
	preview := items
	if previewN > 0 && previewN < len(items) {
		preview = items[:previewN]
	}
	blogData["posts"] = preview
}

// writeBlogPaginatedPages generates /blog/index.html (page 1) and
// /blog/page/N/index.html for each subsequent page.
func writeBlogPaginatedPages(
	logger *slog.Logger,
	opts buildOptions,
	blogTpl sitegen.PageTemplate,
	postList []posts.Post,
	siteData map[string]any,
	assetsDir string,
	mirrorer *externalAssetMirrorer,
) ([]sitemap.URLItem, error) {
	allItems := make([]any, len(postList))
	for i, p := range postList {
		allItems[i] = postToMap(p)
	}
	return writePaginatedPages(logger, opts, blogTpl, allItems, "blog", siteData, assetsDir, mirrorer)
}

// writePostPages renders and writes one HTML file per post using the post template.
func writePostPages(logger *slog.Logger, opts buildOptions, postTpl sitegen.PageTemplate, postList []posts.Post, siteData map[string]any, assetsDir string, mirrorer *externalAssetMirrorer) error {
	allToCopy := make(map[string]string)

	for _, post := range postList {
		templateData := map[string]any{
			"PageName": "post",
			"SiteData": siteData,
			"CurrentPost": map[string]any{
				"title":         post.Meta.Title,
				"date":          post.Meta.Date,
				"summary":       post.Meta.Summary,
				"thumbnail":     post.Meta.Thumbnail,
				"canonical_url": post.Meta.CanonicalURL,
				"tags":          post.Meta.Tags,
				"body_html":     post.BodyHTML,
			},
		}

		var rendered bytes.Buffer
		if err := postTpl.Tmpl.ExecuteTemplate(&rendered, "layout", templateData); err != nil {
			return fmt.Errorf("rendering post %s: %w", post.Meta.Slug, err)
		}

		htmlOut, toCopy, err := transformPage(rendered.String(), opts, assetsDir, mirrorer)
		if err != nil {
			return fmt.Errorf("transforming post %s: %w", post.Meta.Slug, err)
		}
		for k, v := range toCopy {
			allToCopy[k] = v
		}

		target := filepath.Join(opts.outDir, "blog", post.Meta.Slug, "index.html")
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("creating directory for post %s: %w", post.Meta.Slug, err)
		}
		if err := os.WriteFile(target, []byte(htmlOut), 0o644); err != nil { //nolint:gosec
			return fmt.Errorf(errFmtWriting, target, err)
		}
		logger.Info("generated post page", "slug", post.Meta.Slug, "target", target)
	}

	return writeHashedAssets(opts.outDir, assetsDir, allToCopy)
}

// writeRSSFeed generates and writes dist/blog/feed.xml from the loaded posts.
func writeRSSFeed(outDir string, siteData map[string]any, postList []posts.Post) error {
	cfg := feedConfigFromSiteData(siteData)
	xmlBytes, err := rss.GenerateRSS(cfg, postList)
	if err != nil {
		return err
	}

	target := filepath.Join(outDir, "blog", "feed.xml")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("creating blog directory for feed: %w", err)
	}
	if err := os.WriteFile(target, xmlBytes, 0o644); err != nil { //nolint:gosec
		return fmt.Errorf(errFmtWriting, target, err)
	}
	return nil
}

func feedConfigFromSiteData(siteData map[string]any) rss.FeedConfig {
	baseURL, _ := siteData["base_url"].(string)
	htmlLang, _ := siteData["html_lang"].(string)
	if htmlLang == "" {
		htmlLang = "en"
	}

	firstName := ""
	lastName := ""
	if person, ok := siteData["person"].(map[string]any); ok {
		firstName, _ = person["first_name"].(string)
		lastName, _ = person["last_name"].(string)
	}

	name := firstName + " " + lastName
	title := "Writing — " + name
	description := "Articles by " + name

	if blogPage, ok := func() (map[string]any, bool) {
		pages, _ := siteData["pages"].(map[string]any)
		if pages == nil {
			return nil, false
		}
		blog, ok := pages["blog"].(map[string]any)
		return blog, ok
	}(); ok {
		if v, _ := blogPage["title"].(string); v != "" {
			title = v
		}
		if v, _ := blogPage["copy"].(string); v != "" {
			description = v
		}
	}

	return rss.FeedConfig{
		Title:       title,
		Link:        baseURL,
		Description: description,
		Language:    htmlLang,
	}
}
