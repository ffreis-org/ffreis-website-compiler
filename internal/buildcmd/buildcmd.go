package buildcmd

import (
	"encoding/json"
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

	// headEndTag is the closing </head> tag with a preceding newline, used as
	// the injection point for head-level transforms. Defined as a constant to
	// avoid duplicate string literals across multiple injection helpers.
	headEndTag = "\n</head>"
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

	// baseHrefTagRE extracts the href value from a <base href="..."> tag.
	baseHrefTagRE = regexp.MustCompile(`(?is)<base\s+[^>]*href=["']([^"']+)["'][^>]*>`)

	// Regexes for validateRenderedPageStructure.
	titleTagRE        = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	h1TagRE           = regexp.MustCompile(`(?is)<h1[^>]*>(.*?)</h1>`)
	metaDescriptionRE = regexp.MustCompile(`(?is)<meta\s+[^>]*name=["']description["'][^>]*content=["']([^"']*)["'][^>]*>|<meta\s+[^>]*content=["']([^"']*)["'][^>]*name=["']description["'][^>]*>`)
	htmlTagsRE        = regexp.MustCompile(`<[^>]+>`)
)

// optionalContent holds the blog posts, projects, and courses loaded from
// optional input flags, plus the page templates needed to render them.
type optionalContent struct {
	posts        []posts.Post
	projects     []projects.Project
	courses      []courses.Course
	postTemplate *sitegen.PageTemplate
	blogTemplate *sitegen.PageTemplate
}

func Run(args []string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}

	opts, assetsDir, templatesDir, mirrorer, err := prepareBuild(args, logger)
	if err != nil {
		return err
	}

	pages, siteDataResult, _, err := loadAndValidateSiteInputs(logger, opts, templatesDir)
	if err != nil {
		return err
	}

	// Cache base_path on opts so downstream transforms (e.g. fingerprintLocalAssets)
	// can prepend it to root-absolute asset references for deployments served under
	// a path prefix like "/en".
	opts.basePath, _ = siteDataResult.Data["base_path"].(string)

	// Load optional content (posts, projects, courses) and inject into site data.
	// This runs after contract validation so compiler-managed injected data does
	// not need contract entries.
	content, err := loadOptionalContent(opts, siteDataResult.Data)
	if err != nil {
		return err
	}

	// Build dev-data JSON payload once from siteData + content so transformPage
	// can inject it without accessing siteData directly.
	opts.devDataJSON = buildDevDataPayload(opts, siteDataResult.Data, content)

	// Save references to templates needed for paginated page generation,
	// before filterInternalPages removes them.
	findPaginationTemplates(pages, content)

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

	extraSitemapURLs, err := writeAllPaginatedContent(logger, opts, pages, content, siteDataResult.Data, assetsDir, mirrorer)
	if err != nil {
		return err
	}

	// Validate that every internal <a href> link in the compiled output points
	// to a page that was actually generated. This catches broken navigation links
	// (the main cause of "access denied" in production) before S3 promotion.
	// siblingBasePaths lists other deployments sharing the same bucket (e.g. "/en",
	// "/jp") so cross-deployment links in the language switcher are not false-positives.
	// Merge explicitly declared siblings (from -sibling-base-paths flag) with
	// any auto-detected ones from ui.nav.lang_links in the site data.
	siblingBasePaths := append(opts.siblingBasePaths, resolveSiblingBasePaths(siteDataResult.Data)...)
	if err := linkcheck.ValidateAndReport(opts.outDir, opts.basePath, siblingBasePaths); err != nil {
		return fmt.Errorf("link validation: %w", err)
	}
	logger.Info("internal link check passed", "out_dir", opts.outDir)

	if err := maybeGenerateSitemap(logger, opts, templatesDir, pages, extraSitemapURLs); err != nil {
		return err
	}

	logger.Info("build completed", "out_dir", opts.outDir)
	return nil
}

// prepareBuild parses flags, resolves paths, validates directories, and
// initialises the optional asset mirrorer. Extracted from Run to keep Run's
// cognitive complexity within the allowed threshold.
func prepareBuild(args []string, logger *slog.Logger) (opts buildOptions, assetsDir, templatesDir string, mirrorer *externalAssetMirrorer, err error) {
	opts, err = parseBuildOptions(args)
	if err != nil {
		return
	}

	assetsDir, templatesDir, err = resolveBuildPaths(opts)
	if err != nil {
		return
	}

	logBuildStart(logger, opts, assetsDir, templatesDir)

	if err = validateBuildDirs(assetsDir, templatesDir); err != nil {
		return
	}
	if err = ensureOutDir(opts.outDir); err != nil {
		return
	}
	if err = maybeCopyAssets(opts, assetsDir); err != nil {
		return
	}

	mirrorer, err = maybeInitMirrorer(opts)
	return
}

// loadOptionalContent loads blog posts, projects, and courses from the paths
// specified in opts, injecting the data into siteData for template rendering.
func loadOptionalContent(opts buildOptions, siteData map[string]any) (*optionalContent, error) {
	content := &optionalContent{}

	if opts.postsDir != "" {
		loaded, err := posts.LoadPostsDir(opts.postsDir)
		if err != nil {
			return nil, fmt.Errorf("loading blog posts: %w", err)
		}
		if err := posts.CopyPostImages(loaded, opts.outDir); err != nil {
			return nil, fmt.Errorf("copying post images: %w", err)
		}
		content.posts = loaded
		injectPostsBlogList(siteData, loaded, opts.itemsPerPage)
	}

	if opts.projectsFile != "" {
		loaded, err := projects.LoadProjectsFile(opts.projectsFile)
		if err != nil {
			return nil, fmt.Errorf("loading projects: %w", err)
		}
		content.projects = loaded
		injectProjectsHomeCarousel(siteData, loaded, opts.itemsPerPage)
	}

	if opts.coursesFile != "" {
		loaded, err := courses.LoadCoursesFile(opts.coursesFile)
		if err != nil {
			return nil, fmt.Errorf("loading courses: %w", err)
		}
		content.courses = loaded
		injectCoursesHomeCarousel(siteData, loaded, opts.itemsPerPage)
	}

	return content, nil
}

// findPaginationTemplates records the "post" and "blog" page templates from
// pages into content so they are available for paginated page generation after
// filterInternalPages removes them from the slice.
func findPaginationTemplates(pages []sitegen.PageTemplate, content *optionalContent) {
	for i := range pages {
		switch pages[i].Name {
		case "post":
			tmp := pages[i]
			content.postTemplate = &tmp
		case "blog":
			tmp := pages[i]
			content.blogTemplate = &tmp
		}
	}
}

// writeAllPaginatedContent writes individual post pages, the RSS feed, and
// paginated listing pages for blog, projects, and courses. Returns the extra
// sitemap URL items produced by the paginated listings.
func writeAllPaginatedContent(
	logger *slog.Logger,
	opts buildOptions,
	pages []sitegen.PageTemplate,
	content *optionalContent,
	siteData map[string]any,
	assetsDir string,
	mirrorer *externalAssetMirrorer,
) ([]sitemap.URLItem, error) {
	var extraSitemapURLs []sitemap.URLItem

	if err := maybeWritePostContent(logger, opts, content, siteData, assetsDir, mirrorer); err != nil {
		return nil, err
	}

	blogURLs, err := maybeWriteBlogListings(logger, opts, content, siteData, assetsDir, mirrorer)
	if err != nil {
		return nil, err
	}
	extraSitemapURLs = append(extraSitemapURLs, blogURLs...)

	projectURLs, err := maybeWriteProjectListings(logger, opts, pages, content, siteData, assetsDir, mirrorer)
	if err != nil {
		return nil, err
	}
	extraSitemapURLs = append(extraSitemapURLs, projectURLs...)

	courseURLs, err := maybeWriteCourseListings(logger, opts, pages, content, siteData, assetsDir, mirrorer)
	if err != nil {
		return nil, err
	}
	extraSitemapURLs = append(extraSitemapURLs, courseURLs...)

	return extraSitemapURLs, nil
}

// maybeWritePostContent writes individual blog post pages and the RSS feed
// when a post template and loaded posts are available.
func maybeWritePostContent(logger *slog.Logger, opts buildOptions, content *optionalContent, siteData map[string]any, assetsDir string, mirrorer *externalAssetMirrorer) error {
	if content.postTemplate == nil || len(content.posts) == 0 {
		return nil
	}
	if err := ValidatePostLangs(logger, content.posts, siteData); err != nil {
		return fmt.Errorf("validating post languages: %w", err)
	}
	if err := writePostPages(logger, opts, *content.postTemplate, content.posts, siteData, assetsDir, mirrorer); err != nil {
		return fmt.Errorf("writing post pages: %w", err)
	}
	if err := writeRSSFeed(opts.outDir, siteData, content.posts); err != nil {
		return fmt.Errorf("writing rss feed: %w", err)
	}
	return nil
}

// maybeWriteBlogListings generates paginated /blog/ listing pages when a blog
// template and loaded posts are available.
func maybeWriteBlogListings(logger *slog.Logger, opts buildOptions, content *optionalContent, siteData map[string]any, assetsDir string, mirrorer *externalAssetMirrorer) ([]sitemap.URLItem, error) {
	if content.blogTemplate == nil || len(content.posts) == 0 {
		return nil, nil
	}
	urls, err := writeBlogPaginatedPages(logger, opts, *content.blogTemplate, content.posts, siteData, assetsDir, mirrorer)
	if err != nil {
		return nil, fmt.Errorf("writing blog listing pages: %w", err)
	}
	return urls, nil
}

// maybeWriteProjectListings generates paginated /projects/ listing pages when
// projects were loaded and a projects page template exists.
func maybeWriteProjectListings(logger *slog.Logger, opts buildOptions, pages []sitegen.PageTemplate, content *optionalContent, siteData map[string]any, assetsDir string, mirrorer *externalAssetMirrorer) ([]sitemap.URLItem, error) {
	if len(content.projects) == 0 {
		return nil, nil
	}
	tpl := findTemplate(pages, "projects")
	if tpl == nil {
		return nil, nil
	}
	urls, err := writeProjectPages(logger, opts, *tpl, content.projects, siteData, assetsDir, mirrorer)
	if err != nil {
		return nil, fmt.Errorf("writing projects pages: %w", err)
	}
	return urls, nil
}

// maybeWriteCourseListings generates paginated /courses/ listing pages when
// courses were loaded and a courses page template exists.
func maybeWriteCourseListings(logger *slog.Logger, opts buildOptions, pages []sitegen.PageTemplate, content *optionalContent, siteData map[string]any, assetsDir string, mirrorer *externalAssetMirrorer) ([]sitemap.URLItem, error) {
	if len(content.courses) == 0 {
		return nil, nil
	}
	tpl := findTemplate(pages, "courses")
	if tpl == nil {
		return nil, nil
	}
	urls, err := writeCoursePages(logger, opts, *tpl, content.courses, siteData, assetsDir, mirrorer)
	if err != nil {
		return nil, fmt.Errorf("writing courses pages: %w", err)
	}
	return urls, nil
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
		if err := validatePageBaseHref(htmlOut, opts.basePath, slug, page.Name); err != nil {
			return err
		}
		if err := validateRenderedPageStructure(page.Name, htmlOut); err != nil {
			return err
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

// validatePageBaseHref checks that the <base href> in the rendered HTML matches
// the URL where the page will actually be served. A mismatch — typically caused
// by a template using .PageName instead of pageSlug — breaks anchor links and
// relative navigations on pages whose slug differs from the internal page key
// (e.g. the EN "contato" page served at /en/contact/).
func validatePageBaseHref(html, basePath, slug, pageName string) error {
	m := baseHrefTagRE.FindStringSubmatch(html)
	if m == nil {
		return nil // no <base href>; nothing to validate
	}
	got := strings.TrimRight(m[1], "/")
	var want string
	if slug == "index" {
		want = basePath + "/"
		if m[1] == want {
			return nil
		}
	} else {
		want = basePath + "/" + slug
	}
	if got != strings.TrimRight(want, "/") {
		return fmt.Errorf(
			"page %q: <base href=%q> but page is served at %q — template must use pageSlug, not .PageName",
			pageName, m[1], want,
		)
	}
	return nil
}

// validateRenderedPageStructure catches a class of silent data-missing bugs:
// templates that use bare {{dig}} for content fields get an empty string when
// the data key is absent, and the build succeeds with invisible broken content.
// This check fails the build when the resulting HTML has empty structural
// elements that must always carry content.
//
// What it checks (defence-in-depth; templates should use {{required (dig ...)}}
// as the first line of defence):
//   - <title> must be non-empty
//   - <h1> must be non-empty
//   - <meta name="description"> must have a non-empty content attribute
func validateRenderedPageStructure(pageName, html string) error {
	var errs []string

	if m := titleTagRE.FindStringSubmatch(html); m != nil {
		if strings.TrimSpace(htmlTagsRE.ReplaceAllString(m[1], "")) == "" {
			errs = append(errs, "<title> is empty")
		}
	}

	if m := h1TagRE.FindStringSubmatch(html); m != nil {
		if strings.TrimSpace(htmlTagsRE.ReplaceAllString(m[1], "")) == "" {
			errs = append(errs, "<h1> is empty")
		}
	}

	if m := metaDescriptionRE.FindStringSubmatch(html); m != nil {
		content := m[1]
		if content == "" {
			content = m[2]
		}
		if strings.TrimSpace(content) == "" {
			errs = append(errs, `<meta name="description"> content is empty`)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf(
			"page %q has empty structural content (%s) — replace bare {{dig}} calls with {{required (dig ...) \"...\"}} for non-optional fields",
			pageName, strings.Join(errs, "; "),
		)
	}
	return nil
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

// devBuildPayload is the shape of window.__devBuild injected by -dev-data.
type devBuildPayload struct {
	ContentSource string         `json:"contentSource"`
	ContentLangs  []string       `json:"contentLangs"`
	Posts         []devItemEntry `json:"posts"`
	Courses       []devItemEntry `json:"courses"`
	Projects      []devItemEntry `json:"projects"`
}

// devItemEntry carries the title/slug and declared language codes for one
// content item so the dev panel's dynamic tab can filter without a rebuild.
type devItemEntry struct {
	ID    string   `json:"id"`    // slug for posts, title for courses/projects
	Langs []string `json:"langs"` // nil → available in all content languages
}

// buildDevDataPayload serialises the current build's content-source metadata
// and per-item language declarations into a JSON string for window.__devBuild.
// Returns "" when -dev-data is not set.
func buildDevDataPayload(opts buildOptions, siteData map[string]any, content *optionalContent) string {
	if !opts.devData {
		return ""
	}

	// Read content_languages from siteData (set by Phase 4).
	var contentLangs []string
	if cl, ok := siteData["content_languages"].([]any); ok {
		for _, v := range cl {
			if s, ok := v.(string); ok {
				contentLangs = append(contentLangs, s)
			}
		}
	}

	payload := devBuildPayload{
		ContentSource: opts.contentSource,
		ContentLangs:  contentLangs,
		Posts:         make([]devItemEntry, 0, len(content.posts)),
		Courses:       make([]devItemEntry, 0, len(content.courses)),
		Projects:      make([]devItemEntry, 0, len(content.projects)),
	}

	for _, p := range content.posts {
		payload.Posts = append(payload.Posts, devItemEntry{ID: p.Meta.Slug, Langs: p.Meta.AvailableLanguages})
	}
	for _, c := range content.courses {
		payload.Courses = append(payload.Courses, devItemEntry{ID: c.Title, Langs: c.SupportedLanguages})
	}
	for _, p := range content.projects {
		payload.Projects = append(payload.Projects, devItemEntry{ID: p.Title, Langs: nil})
	}

	b, err := json.Marshal(payload)
	if err != nil {
		// json.Marshal cannot fail for these types; return empty to skip injection.
		return ""
	}
	return string(b)
}
