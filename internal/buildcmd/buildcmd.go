package buildcmd

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"ffreis-website-compiler/internal/assetusage"
	"ffreis-website-compiler/internal/courses"
	"ffreis-website-compiler/internal/linkcheck"
	"ffreis-website-compiler/internal/posts"
	"ffreis-website-compiler/internal/projects"
	"ffreis-website-compiler/internal/sitegen"
	"ffreis-website-compiler/internal/sitemap"
)

const (
	sitemapXML  = "sitemap.xml"
	mimeTextCSS = "text/css"
	extWoff2    = ".woff2"

	errFmtWriting = "writing %s: %w"

	// svgInlineSizeLimit is the maximum byte size for an SVG to be inlined as
	// <svg> in HTML. Larger SVGs stay external and are fingerprinted instead.
	svgInlineSizeLimit = 8 * 1024

	// defaultJSInlineThreshold is the default byte limit for inlining <script src>
	// files as <script> blocks. Files at or above this size stay external.
	defaultJSInlineThreshold = 8 * 1024
)

// Package-level regexes — compiled once, shared across all files in this package.
var (
	stylesheetTagRE = regexp.MustCompile(`(?is)<link\s+[^>]*rel=["']stylesheet["'][^>]*href=["']([^"']+)["'][^>]*>`)
	preloadTagRE    = regexp.MustCompile(`(?is)<link\s+[^>]*rel=["'][^"']*preload[^"']*["'][^>]*href=["']([^"']+)["'][^>]*>`)
	scriptTagRE     = regexp.MustCompile(`(?is)<script\s+[^>]*src=["']([^"']+)["'][^>]*>\s*</script>`)
	imgTagRE        = regexp.MustCompile(`(?is)<img\s+[^>]*src=["']([^"']+)["'][^>]*>`)
	iconTagRE       = regexp.MustCompile(`(?is)<link\s+[^>]*rel=["'][^"']*icon[^"']*["'][^>]*href=["']([^"']+)["'][^>]*>`)
	manifestTagRE   = regexp.MustCompile(`(?is)<link\s+[^>]*rel=["']manifest["'][^>]*href=["']([^"']+)["'][^>]*>`)
	cssURLRE        = regexp.MustCompile(`url\(\s*['"]?([^'"\)]+)['"]?\s*\)`)
	cssImportRE     = regexp.MustCompile(`(?is)@import\s+(?:url\(\s*)?['"]?([^'"\)\s;]+)['"]?\s*\)?([^;]*);`)
	xmlProcInstRE   = regexp.MustCompile(`(?i)<\?xml\b[^?]*\?>`)
	svgRootTagRE    = regexp.MustCompile(`(?i)<svg\b[^>]*>`)

	// CSS minification regexes used by minifyCSS.
	cssPreservedCommentRE = regexp.MustCompile(`/\*![\s\S]*?\*/`)
	cssBlockCommentRE     = regexp.MustCompile(`/\*[\s\S]*?\*/`)
	cssDQStringRE         = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)
	cssSQStringRE         = regexp.MustCompile(`'(?:[^'\\]|\\.)*'`)
	cssWhitespaceRE       = regexp.MustCompile(`\s+`)
	cssAroundStructRE     = regexp.MustCompile(`\s*([{};:,])\s*`)

	// headEndRE matches </head> for position-based CSS loading split.
	headEndRE = regexp.MustCompile(`(?i)</head>`)
)

func Run(args []string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}

	opts, err := parseBuildOptions(args)
	if err != nil {
		return err
	}

	assetsDir, templatesDir, err := resolveBuildPaths(opts)
	if err != nil {
		return err
	}

	logBuildStart(logger, opts, assetsDir, templatesDir)

	if err := validateBuildDirs(assetsDir, templatesDir); err != nil {
		return err
	}
	if err := ensureOutDir(opts.outDir); err != nil {
		return err
	}
	if err := maybeCopyAssets(opts, assetsDir); err != nil {
		return err
	}

	mirrorer, err := maybeInitMirrorer(opts)
	if err != nil {
		return err
	}

	pages, siteDataResult, _, err := loadAndValidateSiteInputs(logger, opts, templatesDir)
	if err != nil {
		return err
	}

	// Load and process blog posts (after contract validation so injected data
	// doesn't need contract entries; posts data is compiler-managed).
	var loadedPosts []posts.Post
	var postTemplate *sitegen.PageTemplate
	var blogTemplate *sitegen.PageTemplate
	if opts.postsDir != "" {
		loadedPosts, err = posts.LoadPostsDir(opts.postsDir)
		if err != nil {
			return fmt.Errorf("loading blog posts: %w", err)
		}
		if err := posts.CopyPostImages(loadedPosts, opts.outDir); err != nil {
			return fmt.Errorf("copying post images: %w", err)
		}
		injectPostsBlogList(siteDataResult.Data, loadedPosts, opts.itemsPerPage)
	}

	// Load projects and courses (after contract validation; data is compiler-managed).
	var loadedProjects []projects.Project
	if opts.projectsFile != "" {
		loadedProjects, err = projects.LoadProjectsFile(opts.projectsFile)
		if err != nil {
			return fmt.Errorf("loading projects: %w", err)
		}
		injectProjectsHomeCarousel(siteDataResult.Data, loadedProjects, opts.itemsPerPage)
	}

	var loadedCourses []courses.Course
	if opts.coursesFile != "" {
		loadedCourses, err = courses.LoadCoursesFile(opts.coursesFile)
		if err != nil {
			return fmt.Errorf("loading courses: %w", err)
		}
		injectCoursesHomeCarousel(siteDataResult.Data, loadedCourses, opts.itemsPerPage)
	}

	// Save references to templates needed for paginated page generation,
	// before filterInternalPages removes them.
	for i := range pages {
		switch pages[i].Name {
		case "post":
			tmp := pages[i]
			postTemplate = &tmp
		case "blog":
			tmp := pages[i]
			blogTemplate = &tmp
		}
	}

	// Render all pages (including internal ones) so their CSS/JS assets are
	// seen as "used" by the asset validator. Internal pages are filtered out
	// from disk output and sitemap after validation.
	renderedPages, err := sitegen.RenderPages(pages, siteDataResult.Data)
	if err != nil {
		return err
	}
	if _, err := assetusage.Validate(assetsDir, renderedPages); err != nil {
		return fmt.Errorf("validating local css/js asset usage: %w", err)
	}

	// Cross-page sharing analysis: identify which JS files appear on more than one page.
	// These are candidates for caching rather than per-page inlining when
	// -js-shared-inline-threshold is set. Runs after rendering so the full set of
	// script references (including those injected by partials and the base layout) is visible.
	if opts.jsSharedInlineThreshold >= 0 {
		opts.sharedScripts = collectSharedScripts(renderedPages)
	}

	pages = filterInternalPages(pages, siteDataResult.Data)

	if err := writePages(logger, opts, pages, assetsDir, siteDataResult.Data, renderedPages, mirrorer); err != nil {
		return err
	}

	// Render individual blog post pages and the RSS feed.
	var extraSitemapURLs []sitemap.URLItem
	if postTemplate != nil && len(loadedPosts) > 0 {
		if err := writePostPages(logger, opts, *postTemplate, loadedPosts, siteDataResult.Data, assetsDir, mirrorer); err != nil {
			return fmt.Errorf("writing post pages: %w", err)
		}
		if err := writeRSSFeed(opts.outDir, siteDataResult.Data, loadedPosts); err != nil {
			return fmt.Errorf("writing rss feed: %w", err)
		}
	}

	// Generate paginated listing pages for blog, projects, and courses.
	if blogTemplate != nil && len(loadedPosts) > 0 {
		urls, err := writeBlogPaginatedPages(logger, opts, *blogTemplate, loadedPosts, siteDataResult.Data, assetsDir, mirrorer)
		if err != nil {
			return fmt.Errorf("writing blog listing pages: %w", err)
		}
		extraSitemapURLs = append(extraSitemapURLs, urls...)
	}
	if len(loadedProjects) > 0 {
		projectTpl := findTemplate(pages, "projects")
		if projectTpl != nil {
			urls, err := writeProjectPages(logger, opts, *projectTpl, loadedProjects, siteDataResult.Data, assetsDir, mirrorer)
			if err != nil {
				return fmt.Errorf("writing projects pages: %w", err)
			}
			extraSitemapURLs = append(extraSitemapURLs, urls...)
		}
	}
	if len(loadedCourses) > 0 {
		coursesTpl := findTemplate(pages, "courses")
		if coursesTpl != nil {
			urls, err := writeCoursePages(logger, opts, *coursesTpl, loadedCourses, siteDataResult.Data, assetsDir, mirrorer)
			if err != nil {
				return fmt.Errorf("writing courses pages: %w", err)
			}
			extraSitemapURLs = append(extraSitemapURLs, urls...)
		}
	}

	// Validate that every internal <a href> link in the compiled output points
	// to a page that was actually generated. This catches broken navigation links
	// (the main cause of "access denied" in production) before S3 promotion.
	// siblingBasePaths lists other deployments sharing the same bucket (e.g. "/en",
	// "/jp") so cross-deployment links in the language switcher are not false-positives.
	basePath, _ := siteDataResult.Data["base_path"].(string)
	// Merge explicitly declared siblings (from -sibling-base-paths flag) with
	// any auto-detected ones from ui.nav.lang_links in the site data.
	siblingBasePaths := append(opts.siblingBasePaths, resolveSiblingBasePaths(siteDataResult.Data)...)
	if err := linkcheck.ValidateAndReport(opts.outDir, basePath, siblingBasePaths); err != nil {
		return fmt.Errorf("link validation: %w", err)
	}
	logger.Info("internal link check passed", "out_dir", opts.outDir)

	if err := maybeGenerateSitemap(logger, opts, templatesDir, pages, extraSitemapURLs); err != nil {
		return err
	}

	logger.Info("build completed", "out_dir", opts.outDir)
	return nil
}

func writePages(logger *slog.Logger, opts buildOptions, pages []sitegen.PageTemplate, assetsDir string, siteData map[string]any, renderedPages map[string]string, mirrorer *externalAssetMirrorer) error {
	allToCopy := make(map[string]string) // hashedRelPath → originalRelPath

	for _, page := range pages {
		slug := resolvePageSlug(siteData, page.Name)
		target, err := resolvePageTarget(opts.outDir, slug, opts.cleanURLs)
		if err != nil {
			return err
		}
		htmlOut, pageCopy, err := transformPage(renderedPages[page.Name], opts, assetsDir, mirrorer)
		if err != nil {
			return fmt.Errorf("transforming %s: %w", target, err)
		}
		htmlOut = injectHreflangAlternates(htmlOut, siteData, page.Name, opts.cleanURLs)
		htmlOut = injectLangSwitcherHrefs(htmlOut, siteData, page.Name, opts.cleanURLs)
		for k, v := range pageCopy {
			allToCopy[k] = v
		}
		if err := os.WriteFile(target, []byte(htmlOut), 0o644); err != nil { //nolint:gosec
			return fmt.Errorf(errFmtWriting, target, err)
		}
		logger.Info("generated page", "page", page.Name, "target", target)
		fmt.Fprintln(os.Stdout, target)
	}

	return writeHashedAssets(opts.outDir, assetsDir, allToCopy)
}

func writeHashedAssets(outDir, assetsDir string, assets map[string]string) error {
	for hashedRel, origRel := range assets {
		src := filepath.Join(assetsDir, filepath.FromSlash(origRel))
		dst := filepath.Join(outDir, filepath.FromSlash(hashedRel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("creating dir for %s: %w", hashedRel, err)
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("reading %s for hashed copy: %w", origRel, err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil { //nolint:gosec
			return fmt.Errorf("writing hashed asset %s: %w", hashedRel, err)
		}
	}
	return nil
}

func resolvePageTarget(outDir, pageName string, cleanURLs bool) (string, error) {
	if cleanURLs && pageName != "index" {
		dir := filepath.Join(outDir, pageName)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("creating directory for %s: %w", pageName, err)
		}
		return filepath.Join(dir, "index.html"), nil
	}
	return filepath.Join(outDir, pageName+".html"), nil
}

// findTemplate returns a pointer to the first PageTemplate with the given name,
// or nil if not found.
func findTemplate(pages []sitegen.PageTemplate, name string) *sitegen.PageTemplate {
	for i := range pages {
		if pages[i].Name == name {
			return &pages[i]
		}
	}
	return nil
}

func maybeGenerateSitemap(logger *slog.Logger, opts buildOptions, templatesDir string, pages []sitegen.PageTemplate, extraURLs []sitemap.URLItem) error {
	sitemapConfigPath, err := resolveSitemapConfigPath(opts.websiteRoot, opts.sitemapConfig)
	if err != nil {
		return err
	}

	if sitemapConfigPath != "" {
		if err := generateSitemapFromConfig(sitemapConfigPath, opts.websiteRoot, opts.outDir, extraURLs); err != nil {
			return err
		}
		logger.Info("generated sitemap from config", "config_path", sitemapConfigPath, "target", filepath.Join(opts.outDir, sitemapXML))
		fmt.Fprintln(os.Stdout, filepath.Join(opts.outDir, sitemapXML))
		return nil
	}

	baseURL := strings.TrimSpace(opts.sitemapBaseURL)
	if baseURL == "" {
		return nil
	}
	if err := generateSitemapFromPages(baseURL, templatesDir, pages, opts.outDir, opts.cleanURLs, extraURLs); err != nil {
		return err
	}
	logger.Info("generated sitemap from pages", "base_url", baseURL, "target", filepath.Join(opts.outDir, sitemapXML))
	fmt.Fprintln(os.Stdout, filepath.Join(opts.outDir, sitemapXML))
	return nil
}

func resolveSitemapConfigPath(websiteRoot, flagPath string) (string, error) {
	if strings.TrimSpace(flagPath) != "" {
		if _, err := os.Stat(flagPath); err != nil {
			return "", fmt.Errorf("sitemap config not found: %s (%w)", flagPath, err)
		}
		return flagPath, nil
	}

	defaultPath := filepath.Join(websiteRoot, "sitemap.yaml")
	if _, err := os.Stat(defaultPath); err == nil {
		return defaultPath, nil
	}

	return "", nil
}

func generateSitemapFromConfig(configPath, websiteRoot, outDir string, extraURLs []sitemap.URLItem) error {
	cfg, err := sitemap.LoadConfig(configPath)
	if err != nil {
		return err
	}
	cfg.URLs = append(cfg.URLs, extraURLs...)

	xmlBytes, err := sitemap.GenerateXML(cfg, websiteRoot)
	if err != nil {
		return err
	}

	targetPath := filepath.Join(outDir, sitemapXML)
	if err := os.WriteFile(targetPath, xmlBytes, 0o644); err != nil { //nolint:gosec
		return fmt.Errorf(errFmtWriting, sitemapXML, err)
	}
	return nil
}

func generateSitemapFromPages(baseURL, templatesDir string, pages []sitegen.PageTemplate, outDir string, cleanURLs bool, extraURLs []sitemap.URLItem) error {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return fmt.Errorf("sitemap base URL cannot be empty")
	}

	urls := make([]sitemap.URLItem, 0, len(pages)+len(extraURLs))
	for _, page := range pages {
		var path string
		if page.Name == "index" {
			path = "/"
		} else if cleanURLs {
			path = "/" + page.Name
		} else {
			path = "/" + page.Name + ".html"
		}

		item := sitemap.URLItem{Path: path}
		pageTemplatePath := filepath.Join(templatesDir, "pages", page.Name+".gohtml")
		if info, err := os.Stat(pageTemplatePath); err == nil {
			item.Lastmod = info.ModTime().In(time.UTC).Format("2006-01-02")
		}
		urls = append(urls, item)
	}
	urls = append(urls, extraURLs...)

	cfg := sitemap.Config{
		BaseURL: strings.TrimRight(baseURL, "/"),
		URLs:    urls,
	}

	xmlBytes, err := sitemap.GenerateXML(cfg, "")
	if err != nil {
		return err
	}

	targetPath := filepath.Join(outDir, sitemapXML)
	if err := os.WriteFile(targetPath, xmlBytes, 0o644); err != nil { //nolint:gosec
		return fmt.Errorf(errFmtWriting, sitemapXML, err)
	}

	return nil
}

// collectSharedScripts scans every rendered page for local <script src="..."> references
// and returns the set of asset paths (without leading "/") that appear on more than one page.
// These scripts benefit from being cached externally rather than inlined into every HTML file.
func collectSharedScripts(renderedPages map[string]string) map[string]bool {
	usageCount := make(map[string]int, 16)
	for _, html := range renderedPages {
		matches := scriptTagRE.FindAllStringSubmatch(html, -1)
		for _, m := range matches {
			src := m[1]
			if isExternalRef(src) {
				continue
			}
			usageCount[strings.TrimPrefix(src, "/")]++
		}
	}
	shared := make(map[string]bool, len(usageCount))
	for path, n := range usageCount {
		if n > 1 {
			shared[path] = true
		}
	}
	return shared
}
