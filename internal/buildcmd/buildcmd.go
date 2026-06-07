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

	opts, assetsDir, templatesDir, mirrorer, pages, siteDataResult, err := setupBuild(args, logger)
	if err != nil {
		return err
	}

	content, err := loadAllContent(opts, pages, siteDataResult.Data)
	if err != nil {
		return err
	}

	renderedPages, pages, err := renderAndValidatePages(opts, pages, assetsDir, siteDataResult.Data)
	if err != nil {
		return err
	}

	env := writeEnv{logger: logger, opts: opts, pages: pages, siteData: siteDataResult.Data, assetsDir: assetsDir, mirrorer: mirrorer}

	if err := writePages(logger, opts, pages, assetsDir, siteDataResult.Data, renderedPages, mirrorer); err != nil {
		return err
	}

	extraSitemapURLs, err := writeAllPaginatedContent(env, content)
	if err != nil {
		return err
	}

	return finalizeBuild(logger, opts, templatesDir, pages, siteDataResult.Data, extraSitemapURLs)
}

// setupBuild parses options, resolves paths, validates directories, copies assets,
// initialises the mirrorer, and loads+validates site inputs. It is the non-content
// portion of Run extracted to reduce Run's cognitive complexity.
func setupBuild(args []string, logger *slog.Logger) (buildOptions, string, string, *externalAssetMirrorer, []sitegen.PageTemplate, sitegen.SiteDataLoadResult, error) {
	opts, err := parseBuildOptions(args)
	if err != nil {
		return buildOptions{}, "", "", nil, nil, sitegen.SiteDataLoadResult{}, err
	}

	assetsDir, templatesDir, err := resolveBuildPaths(opts)
	if err != nil {
		return buildOptions{}, "", "", nil, nil, sitegen.SiteDataLoadResult{}, err
	}

	logBuildStart(logger, opts, assetsDir, templatesDir)

	if err := validateBuildDirs(assetsDir, templatesDir); err != nil {
		return buildOptions{}, "", "", nil, nil, sitegen.SiteDataLoadResult{}, err
	}
	if err := ensureOutDir(opts.outDir); err != nil {
		return buildOptions{}, "", "", nil, nil, sitegen.SiteDataLoadResult{}, err
	}
	if err := maybeCopyAssets(opts, assetsDir); err != nil {
		return buildOptions{}, "", "", nil, nil, sitegen.SiteDataLoadResult{}, err
	}

	mirrorer, err := maybeInitMirrorer(opts)
	if err != nil {
		return buildOptions{}, "", "", nil, nil, sitegen.SiteDataLoadResult{}, err
	}

	pages, siteDataResult, _, err := loadAndValidateSiteInputs(logger, opts, templatesDir)
	if err != nil {
		return buildOptions{}, "", "", nil, nil, sitegen.SiteDataLoadResult{}, err
	}

	// Cache base_path so downstream transforms can prepend it to root-absolute
	// asset references for deployments served under a path prefix like "/en".
	opts.basePath, _ = siteDataResult.Data["base_path"].(string)

	return opts, assetsDir, templatesDir, mirrorer, pages, siteDataResult, nil
}

// finalizeBuild runs link validation, sitemap generation, and logs completion.
func finalizeBuild(logger *slog.Logger, opts buildOptions, templatesDir string, pages []sitegen.PageTemplate, siteData map[string]any, extraSitemapURLs []sitemap.URLItem) error {
	siblingBasePaths := append(opts.siblingBasePaths, resolveSiblingBasePaths(siteData)...)
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

// loadAllContent loads posts, projects, and courses after contract validation, injects
// them into siteData, and saves template pointers needed for paginated generation.
func loadAllContent(opts buildOptions, pages []sitegen.PageTemplate, siteData map[string]any) (loadedContent, error) {
	var c loadedContent

	if opts.postsDir != "" {
		var err error
		c.posts, err = posts.LoadPostsDir(opts.postsDir)
		if err != nil {
			return c, fmt.Errorf("loading blog posts: %w", err)
		}
		if err := posts.CopyPostImages(c.posts, opts.outDir); err != nil {
			return c, fmt.Errorf("copying post images: %w", err)
		}
		injectPostsBlogList(siteData, c.posts, opts.itemsPerPage)
	}

	if opts.projectsFile != "" {
		var err error
		c.projects, err = projects.LoadProjectsFile(opts.projectsFile)
		if err != nil {
			return c, fmt.Errorf("loading projects: %w", err)
		}
		injectProjectsHomeCarousel(siteData, c.projects, opts.itemsPerPage)
	}

	if opts.coursesFile != "" {
		var err error
		c.courses, err = courses.LoadCoursesFile(opts.coursesFile)
		if err != nil {
			return c, fmt.Errorf("loading courses: %w", err)
		}
		injectCoursesHomeCarousel(siteData, c.courses, opts.itemsPerPage)
	}

	// Save template pointers before filterInternalPages removes them.
	for i := range pages {
		switch pages[i].Name {
		case "post":
			tmp := pages[i]
			c.postTemplate = &tmp
		case "blog":
			tmp := pages[i]
			c.blogTemplate = &tmp
		}
	}
	return c, nil
}

// renderAndValidatePages renders all page templates, validates asset usage, computes
// shared scripts, and returns the rendered HTML map plus the filtered page list.
func renderAndValidatePages(opts buildOptions, pages []sitegen.PageTemplate, assetsDir string, siteData map[string]any) (map[string]string, []sitegen.PageTemplate, error) {
	renderedPages, err := sitegen.RenderPages(pages, siteData)
	if err != nil {
		return nil, nil, err
	}
	if _, err := assetusage.Validate(assetsDir, renderedPages); err != nil {
		return nil, nil, fmt.Errorf("validating local css/js asset usage: %w", err)
	}
	if opts.jsSharedInlineThreshold >= 0 {
		opts.sharedScripts = collectSharedScripts(renderedPages)
	}
	pages = filterInternalPages(pages, siteData)
	return renderedPages, pages, nil
}

// loadedContent bundles the content loaded from external sources after contract validation.
type loadedContent struct {
	posts        []posts.Post
	projects     []projects.Project
	courses      []courses.Course
	postTemplate *sitegen.PageTemplate
	blogTemplate *sitegen.PageTemplate
}

// writeEnv bundles the shared write-time arguments to avoid long parameter lists.
type writeEnv struct {
	logger    *slog.Logger
	opts      buildOptions
	pages     []sitegen.PageTemplate
	siteData  map[string]any
	assetsDir string
	mirrorer  *externalAssetMirrorer
}

// writeAllPaginatedContent writes post pages, RSS feed, and paginated listing pages.
// Returns sitemap URL entries for all generated pages.
func writeAllPaginatedContent(env writeEnv, content loadedContent) ([]sitemap.URLItem, error) {
	if content.postTemplate != nil && len(content.posts) > 0 {
		if err := writePostPages(env.logger, env.opts, *content.postTemplate, content.posts, env.siteData, env.assetsDir, env.mirrorer); err != nil {
			return nil, fmt.Errorf("writing post pages: %w", err)
		}
		if err := writeRSSFeed(env.opts.outDir, env.siteData, content.posts); err != nil {
			return nil, fmt.Errorf("writing rss feed: %w", err)
		}
	}
	return writePaginatedIfNeeded(env, content)
}

// writePaginatedIfNeeded writes paginated listing pages for blog, projects, and courses.
func writePaginatedIfNeeded(env writeEnv, content loadedContent) ([]sitemap.URLItem, error) {
	var urls []sitemap.URLItem

	if content.blogTemplate != nil && len(content.posts) > 0 {
		blogURLs, err := writeBlogPaginatedPages(env.logger, env.opts, *content.blogTemplate, content.posts, env.siteData, env.assetsDir, env.mirrorer)
		if err != nil {
			return nil, fmt.Errorf("writing blog listing pages: %w", err)
		}
		urls = append(urls, blogURLs...)
	}
	if len(content.projects) > 0 {
		if tpl := findTemplate(env.pages, "projects"); tpl != nil {
			projectURLs, err := writeProjectPages(env.logger, env.opts, *tpl, content.projects, env.siteData, env.assetsDir, env.mirrorer)
			if err != nil {
				return nil, fmt.Errorf("writing projects pages: %w", err)
			}
			urls = append(urls, projectURLs...)
		}
	}
	if len(content.courses) > 0 {
		if tpl := findTemplate(env.pages, "courses"); tpl != nil {
			courseURLs, err := writeCoursePages(env.logger, env.opts, *tpl, content.courses, env.siteData, env.assetsDir, env.mirrorer)
			if err != nil {
				return nil, fmt.Errorf("writing courses pages: %w", err)
			}
			urls = append(urls, courseURLs...)
		}
	}
	return urls, nil
}
