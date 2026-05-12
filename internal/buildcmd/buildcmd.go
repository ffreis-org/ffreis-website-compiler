package buildcmd

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"ffreis-website-compiler/internal/assetusage"
	"ffreis-website-compiler/internal/cmdutil"
	"ffreis-website-compiler/internal/posts"
	"ffreis-website-compiler/internal/sitegen"
	"ffreis-website-compiler/internal/sitemap"
)

const (
	sitemapXML  = "sitemap.xml"
	mimeTextCSS = "text/css"
	extWoff2    = ".woff2"

	errFmtWriting = "writing %s: %w"
)

var (
	stylesheetTagRE = regexp.MustCompile(`(?is)<link\s+[^>]*rel=["']stylesheet["'][^>]*href=["']([^"']+)["'][^>]*>`)
	preloadTagRE    = regexp.MustCompile(`(?is)<link\s+[^>]*rel=["'][^"']*preload[^"']*["'][^>]*href=["']([^"']+)["'][^>]*>`)
	scriptTagRE     = regexp.MustCompile(`(?is)<script\s+[^>]*src=["']([^"']+)["'][^>]*>\s*</script>`)
	imgTagRE        = regexp.MustCompile(`(?is)<img\s+[^>]*src=["']([^"']+)["'][^>]*>`)
	iconTagRE       = regexp.MustCompile(`(?is)<link\s+[^>]*rel=["'][^"']*icon[^"']*["'][^>]*href=["']([^"']+)["'][^>]*>`)
	manifestTagRE   = regexp.MustCompile(`(?is)<link\s+[^>]*rel=["']manifest["'][^>]*href=["']([^"']+)["'][^>]*>`)
	cssURLRE        = regexp.MustCompile(`url\(\s*['"]?([^'"\)]+)['"]?\s*\)`)
	cssImportRE     = regexp.MustCompile(`(?is)@import\s+(?:url\(\s*)?['"]?([^'"\)\s;]+)['"]?\s*\)?([^;]*);`)
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
	if opts.postsDir != "" {
		loadedPosts, err = posts.LoadPostsDir(opts.postsDir)
		if err != nil {
			return fmt.Errorf("loading blog posts: %w", err)
		}
		if err := posts.CopyPostImages(loadedPosts, opts.outDir); err != nil {
			return fmt.Errorf("copying post images: %w", err)
		}
		injectPostsBlogList(siteDataResult.Data, loadedPosts)
	}

	// Save a reference to the post template before filterInternalPages removes it.
	for i := range pages {
		if pages[i].Name == "post" {
			tmp := pages[i]
			postTemplate = &tmp
			break
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

	pages = filterInternalPages(pages, siteDataResult.Data)

	if err := writePages(logger, opts, pages, assetsDir, renderedPages, mirrorer); err != nil {
		return err
	}

	// Render individual blog post pages and the RSS feed.
	if postTemplate != nil && len(loadedPosts) > 0 {
		if err := writePostPages(logger, opts, *postTemplate, loadedPosts, siteDataResult.Data, assetsDir, mirrorer); err != nil {
			return fmt.Errorf("writing post pages: %w", err)
		}
		if err := writeRSSFeed(opts.outDir, siteDataResult.Data, loadedPosts); err != nil {
			return fmt.Errorf("writing rss feed: %w", err)
		}
	}

	if err := maybeGenerateSitemap(logger, opts, templatesDir, pages); err != nil {
		return err
	}

	logger.Info("build completed", "out_dir", opts.outDir)
	return nil
}

type buildOptions struct {
	websiteRoot          string
	assetsDir            string
	templatesDir         string
	sitemapConfig        string
	sitemapBaseURL       string
	siteDataSource       string
	outDir               string
	postsDir             string
	copyAssets           bool
	inlineAssets         bool
	mirrorExternalAssets bool
	mirroredAssetsDir    string
	enableSanity         bool
	strictContract       bool
	cleanURLs            bool
}

func parseBuildOptions(args []string) (buildOptions, error) {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)

	var opts buildOptions
	var assetsDirFlag string
	var siteDirFlag string
	var templatesDirFlag string

	fs.StringVar(&opts.websiteRoot, "website-root", ".", "website project root; expects <website-root>/src/{assets,templates} (legacy fallback: <website-root>/{site,templates})")
	fs.StringVar(&assetsDirFlag, "assets-dir", "", "path to source assets folder (defaults to <website-root>/src/assets, then <website-root>/site)")
	fs.StringVar(&siteDirFlag, "site-dir", "", "legacy alias for -assets-dir")
	fs.StringVar(&templatesDirFlag, "templates-dir", "", "path to templates root folder (defaults to <website-root>/src/templates, then <website-root>/templates)")
	fs.StringVar(&opts.sitemapConfig, "sitemap-config", "", "optional path to sitemap YAML config; defaults to <website-root>/sitemap.yaml if present")
	fs.StringVar(&opts.sitemapBaseURL, "sitemap-base-url", "", "optional base URL for automatic sitemap.xml generation when no sitemap config file is found")
	fs.StringVar(&opts.siteDataSource, "site-data", "", "optional site data source override; supports file/URL sources or a directory containing YAML layers")
	fs.StringVar(&opts.outDir, "out", "dist", "output directory for generated static site")
	fs.BoolVar(&opts.copyAssets, "copy-assets", true, "copy static assets from assets dir into output")
	fs.BoolVar(&opts.inlineAssets, "inline-assets", false, "inline local css/js/images into each html for self-contained pages")
	fs.BoolVar(&opts.mirrorExternalAssets, "mirror-external-assets", false, "download external css/js/image/font assets into output and rewrite references to local copies")
	fs.StringVar(&opts.mirroredAssetsDir, "mirrored-assets-dir", "external", "subdirectory inside output for mirrored external assets")
	fs.BoolVar(&opts.enableSanity, "sanity", true, "fail the build if generic sanity checks fail (site contract + invariants + asset reachability)")
	fs.BoolVar(&opts.strictContract, "strict-contract", true, "fail if any allowed contract path is not referenced by any template (disable for local dev with in-progress templates)")
	fs.BoolVar(&opts.cleanURLs, "clean-urls", false, "output each page as <name>/index.html instead of <name>.html for extension-free URLs; updates sitemap accordingly")
	fs.StringVar(&opts.postsDir, "posts-dir", "", "path to blog posts directory (posts/<slug>/index.md layout); enables Markdown blog post generation and RSS feed when set")

	if err := fs.Parse(args); err != nil {
		return buildOptions{}, err
	}

	if assetsDirFlag == "" && siteDirFlag != "" {
		assetsDirFlag = siteDirFlag
	}
	opts.assetsDir = assetsDirFlag
	opts.templatesDir = templatesDirFlag

	return opts, nil
}

func resolveBuildPaths(opts buildOptions) (assetsDir string, templatesDir string, err error) {
	assetsDir = opts.assetsDir
	templatesDir = opts.templatesDir
	if assetsDir != "" && templatesDir != "" {
		return assetsDir, templatesDir, nil
	}

	resolvedAssetsDir, resolvedTemplatesDir, err := cmdutil.ResolveWebsitePaths(opts.websiteRoot)
	if err != nil {
		return "", "", err
	}

	if assetsDir == "" {
		assetsDir = resolvedAssetsDir
	}
	if templatesDir == "" {
		templatesDir = resolvedTemplatesDir
	}

	return assetsDir, templatesDir, nil
}

func logBuildStart(logger *slog.Logger, opts buildOptions, assetsDir, templatesDir string) {
	logger.Info(
		"starting website build",
		"website_root", opts.websiteRoot,
		"assets_dir", assetsDir,
		"templates_dir", templatesDir,
		"out_dir", opts.outDir,
		"copy_assets", opts.copyAssets,
		"inline_assets", opts.inlineAssets,
		"mirror_external_assets", opts.mirrorExternalAssets,
		"mirrored_assets_dir", opts.mirroredAssetsDir,
	)
}

func validateBuildDirs(assetsDir, templatesDir string) error {
	if _, err := os.Stat(assetsDir); err != nil {
		return fmt.Errorf("assets directory not found: %s (%w)", assetsDir, err)
	}
	if _, err := os.Stat(templatesDir); err != nil {
		return fmt.Errorf("templates directory not found: %s (%w)", templatesDir, err)
	}
	return nil
}

func ensureOutDir(outDir string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}
	return nil
}

func maybeCopyAssets(opts buildOptions, assetsDir string) error {
	if !opts.copyAssets || opts.inlineAssets {
		return nil
	}
	if err := copyStaticAssets(assetsDir, opts.outDir); err != nil {
		return fmt.Errorf("copying assets: %w", err)
	}
	return nil
}

func maybeInitMirrorer(opts buildOptions) (*externalAssetMirrorer, error) {
	if !opts.mirrorExternalAssets {
		return nil, nil
	}

	mirrorer := newExternalAssetMirrorer(opts.outDir, opts.mirroredAssetsDir)
	if opts.copyAssets && !opts.inlineAssets {
		if err := mirrorExternalAssetsInCopiedCSS(filepath.Join(opts.outDir, "css"), mirrorer); err != nil {
			return nil, fmt.Errorf("mirroring external assets in copied css: %w", err)
		}
	}
	return mirrorer, nil
}

func loadAndValidateSiteInputs(logger *slog.Logger, opts buildOptions, templatesDir string) ([]sitegen.PageTemplate, sitegen.SiteDataLoadResult, sitegen.SiteDataContractLoadResult, error) {
	var (
		pages                  []sitegen.PageTemplate
		siteDataResult         sitegen.SiteDataLoadResult
		siteDataContractResult sitegen.SiteDataContractLoadResult
		err                    error
	)

	pages, siteDataResult, siteDataContractResult, err = loadSiteDataWithOptionalUsageCheck(logger, templatesDir, opts.siteDataSource, opts.strictContract)
	if err != nil {
		return nil, sitegen.SiteDataLoadResult{}, sitegen.SiteDataContractLoadResult{}, err
	}

	if opts.enableSanity {
		if err := sitegen.ValidateSiteSanity(siteDataResult.Data, sitegen.DefaultSanityConfig()); err != nil {
			return nil, sitegen.SiteDataLoadResult{}, sitegen.SiteDataContractLoadResult{}, fmt.Errorf("validating site sanity rules: %w", err)
		}
	}
	logger.Info("loaded templates", "count", len(pages), "templates_dir", templatesDir)
	return pages, siteDataResult, siteDataContractResult, nil
}

// loadSiteDataWithOptionalUsageCheck loads templates and data, validates site
// data against the contract, and optionally validates contract usage by
// templates. The build command treats pages.<name>.internal as compiler-managed
// metadata and excludes it from contract checks.
func loadSiteDataWithOptionalUsageCheck(logger *slog.Logger, templatesDir, siteDataSource string, validateUsage bool) ([]sitegen.PageTemplate, sitegen.SiteDataLoadResult, sitegen.SiteDataContractLoadResult, error) {
	pages, err := sitegen.LoadPageTemplatesFromRoot(templatesDir)
	if err != nil {
		return nil, sitegen.SiteDataLoadResult{}, sitegen.SiteDataContractLoadResult{}, fmt.Errorf("loading templates: %w", err)
	}
	siteDataResult, err := sitegen.LoadSiteData(templatesDir, siteDataSource)
	if err != nil {
		return nil, sitegen.SiteDataLoadResult{}, sitegen.SiteDataContractLoadResult{}, fmt.Errorf("loading site data: %w", err)
	}
	siteDataContractResult, err := sitegen.LoadSiteDataContract(templatesDir)
	if err != nil {
		return nil, sitegen.SiteDataLoadResult{}, sitegen.SiteDataContractLoadResult{}, fmt.Errorf("loading site data contract: %w", err)
	}
	cmdutil.LogSiteDataOverride(logger, siteDataResult)
	contract := contractWithoutPageInternalPatterns(siteDataContractResult.Contract)
	siteDataForValidation := siteDataWithoutPageInternalFlags(siteDataResult.Data)
	if err := sitegen.ValidateSiteData(siteDataForValidation, contract); err != nil {
		return nil, sitegen.SiteDataLoadResult{}, sitegen.SiteDataContractLoadResult{}, fmt.Errorf("validating site data against contract: %w", err)
	}
	if validateUsage {
		usedPaths, err := sitegen.TraceSiteDataUsage(pages, siteDataResult.Data)
		if err != nil {
			return nil, sitegen.SiteDataLoadResult{}, sitegen.SiteDataContractLoadResult{}, fmt.Errorf("tracing site data usage: %w", err)
		}
		if err := sitegen.ValidateSiteDataContractUsage(contract, usedPaths); err != nil {
			return nil, sitegen.SiteDataLoadResult{}, sitegen.SiteDataContractLoadResult{}, fmt.Errorf("validating site data contract usage: %w", err)
		}
	}
	return pages, siteDataResult, siteDataContractResult, nil
}

// siteDataWithoutPageInternalFlags returns a shallow copy of siteData where
// the "internal" key has been removed from every page entry. Only the two
// affected levels (top-level map and the pages map) are copied; deeper values
// are shared and never mutated.
func siteDataWithoutPageInternalFlags(siteData map[string]any) map[string]any {
	pagesData, ok := siteData["pages"].(map[string]any)
	if !ok {
		return siteData
	}

	newPages := make(map[string]any, len(pagesData))
	for name, pageData := range pagesData {
		pageMap, ok := pageData.(map[string]any)
		if !ok {
			newPages[name] = pageData
			continue
		}
		copied := make(map[string]any, len(pageMap))
		for k, v := range pageMap {
			copied[k] = v
		}
		delete(copied, "internal")
		newPages[name] = copied
	}

	result := make(map[string]any, len(siteData))
	for k, v := range siteData {
		result[k] = v
	}
	result["pages"] = newPages
	return result
}

func contractWithoutPageInternalPatterns(contract sitegen.SiteDataContract) sitegen.SiteDataContract {
	contract.Required = contractPatternsWithoutPageInternal(contract.Required)
	contract.Allowed = contractPatternsWithoutPageInternal(contract.Allowed)
	return contract
}

func contractPatternsWithoutPageInternal(patterns []string) []string {
	filtered := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		if !isPageInternalPattern(pattern) {
			filtered = append(filtered, pattern)
		}
	}
	return filtered
}

func isPageInternalPattern(pattern string) bool {
	parts := strings.Split(strings.TrimSpace(pattern), ".")
	if len(parts) != 3 || parts[0] != "pages" || parts[2] != "internal" {
		return false
	}
	segment := parts[1]
	if segment == "*" {
		return true
	}
	return segment != "" && !strings.Contains(segment, "*")
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

// injectNavigationEnhancements inserts two <head> elements into every rendered page:
//   - Cross-document view-transition CSS: eliminates the flash-of-unstyled-content on
//     page navigation by fading between pages instead of doing a hard repaint.
//   - Speculation Rules JSON: tells supporting browsers to prerender same-origin pages
//     the user is likely to visit (on hover, eagerness=moderate), making navigations
//     feel near-instant.
//
// Both features degrade silently in unsupported browsers, so no flag is provided.
func injectNavigationEnhancements(html string) string {
	const inject = `<style>@view-transition{navigation:auto}</style>` +
		"\n    " +
		`<script type="speculationrules">{"prerender":[{"where":{"href_matches":"/*"},"eagerness":"moderate"}]}</script>`
	return strings.Replace(html, "</head>", inject+"\n</head>", 1)
}

func writePages(logger *slog.Logger, opts buildOptions, pages []sitegen.PageTemplate, assetsDir string, renderedPages map[string]string, mirrorer *externalAssetMirrorer) error {
	allToCopy := make(map[string]string) // hashedRelPath → originalRelPath

	for _, page := range pages {
		target, err := resolvePageTarget(opts.outDir, page.Name, opts.cleanURLs)
		if err != nil {
			return err
		}
		htmlOut, pageCopy, err := transformPage(renderedPages[page.Name], opts, assetsDir, mirrorer)
		if err != nil {
			return fmt.Errorf("transforming %s: %w", target, err)
		}
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
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil { //nolint:gosec
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

func transformPage(html string, opts buildOptions, assetsDir string, mirrorer *externalAssetMirrorer) (string, map[string]string, error) {
	html = injectNavigationEnhancements(html)

	if opts.inlineAssets {
		// Full asset inlining (CSS + JS + images). Converts url() to data URIs so the
		// page is fully self-contained. Does not fingerprint (data URIs need no cache key).
		updated, err := inlineLocalAssets(html, assetsDir)
		if err != nil {
			return "", nil, fmt.Errorf("inlining assets: %w", err)
		}
		html = updated
	} else {
		// Position-based CSS loading:
		//   head  CSS → inlined as <style> (critical, zero-latency, render-correct)
		//   body  CSS → kept external with media="print" onload trick (deferred, non-blocking)
		// Mirrors the JS-at-end convention: document position signals loading priority.
		updated, err := transformStylesheets(html, assetsDir)
		if err != nil {
			return "", nil, fmt.Errorf("transforming stylesheets: %w", err)
		}
		html = updated
	}

	// LQIP: generate blurry thumbnails for above-fold images and swap to full on load.
	lqipHTML, err := processLQIPImages(html, assetsDir)
	if err != nil {
		return "", nil, fmt.Errorf("processing LQIP images: %w", err)
	}
	html = lqipHTML

	// Fingerprint all remaining external local asset references so CloudFront
	// can serve them with immutable (1-year) cache headers.
	fingerprintedHTML, toCopy, err := fingerprintLocalAssets(html, assetsDir)
	if err != nil {
		return "", nil, fmt.Errorf("fingerprinting assets: %w", err)
	}
	html = fingerprintedHTML

	if mirrorer != nil {
		updated, err := mirrorer.rewriteHTML(html)
		if err != nil {
			return "", nil, fmt.Errorf("mirroring external assets: %w", err)
		}
		html = updated
	}

	return html, toCopy, nil
}

func maybeGenerateSitemap(logger *slog.Logger, opts buildOptions, templatesDir string, pages []sitegen.PageTemplate) error {
	sitemapConfigPath, err := resolveSitemapConfigPath(opts.websiteRoot, opts.sitemapConfig)
	if err != nil {
		return err
	}

	if sitemapConfigPath != "" {
		if err := generateSitemapFromConfig(sitemapConfigPath, opts.websiteRoot, opts.outDir); err != nil {
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
	if err := generateSitemapFromPages(baseURL, templatesDir, pages, opts.outDir, opts.cleanURLs); err != nil {
		return err
	}
	logger.Info("generated sitemap from pages", "base_url", baseURL, "target", filepath.Join(opts.outDir, sitemapXML))
	fmt.Fprintln(os.Stdout, filepath.Join(opts.outDir, sitemapXML))
	return nil
}

type externalAssetMirrorer struct {
	client     *http.Client
	outDir     string
	assetsDir  string
	cache      map[string]string
	inProgress map[string]string
}

func newExternalAssetMirrorer(outDir, assetsDir string) *externalAssetMirrorer {
	return &externalAssetMirrorer{
		client:     &http.Client{Timeout: 30 * time.Second},
		outDir:     outDir,
		assetsDir:  strings.Trim(strings.TrimSpace(filepath.ToSlash(assetsDir)), "/"),
		cache:      make(map[string]string),
		inProgress: make(map[string]string),
	}
}

func (m *externalAssetMirrorer) rewriteHTML(doc string) (string, error) {
	var err error

	doc, err = replaceTagWith(doc, stylesheetTagRE, func(tag string, refs []string) (string, error) {
		return m.replaceExternalRef(tag, refs[1], ".css")
	})
	if err != nil {
		return "", err
	}

	doc, err = replaceTagWith(doc, preloadTagRE, func(tag string, refs []string) (string, error) {
		return m.replaceExternalRef(tag, refs[1], hintedExtFromPreload(tag))
	})
	if err != nil {
		return "", err
	}

	doc, err = replaceTagWith(doc, scriptTagRE, func(tag string, refs []string) (string, error) {
		return m.replaceExternalRef(tag, refs[1], ".js")
	})
	if err != nil {
		return "", err
	}

	doc, err = replaceTagWith(doc, iconTagRE, func(tag string, refs []string) (string, error) {
		return m.replaceExternalRef(tag, refs[1], "")
	})
	if err != nil {
		return "", err
	}

	doc, err = replaceTagWith(doc, imgTagRE, func(tag string, refs []string) (string, error) {
		return m.replaceExternalRef(tag, refs[1], "")
	})
	if err != nil {
		return "", err
	}

	// Mirror external url() refs inside inline <style> blocks. These arise when CSS
	// is inlined by the compiler (inlineLocalStylesheetsPreserveURLs) and the original
	// CSS file contained external http:// background-image or other url() references.
	doc = styleBlockRE.ReplaceAllStringFunc(doc, func(block string) string {
		if err != nil {
			return block
		}
		parts := styleBlockRE.FindStringSubmatch(block)
		if parts == nil {
			return block
		}
		rewritten, e := m.rewriteCSS(parts[2], nil)
		if e != nil {
			err = e
			return block
		}
		return parts[1] + rewritten + parts[3]
	})
	if err != nil {
		return "", err
	}

	return doc, nil
}

func (m *externalAssetMirrorer) replaceExternalRef(tag, ref, hintedExt string) (string, error) {
	absoluteURL, ok := resolveExternalURL(nil, ref)
	if !ok {
		return tag, nil
	}
	localRef, err := m.mirrorURL(absoluteURL, hintedExt)
	if err != nil {
		return "", err
	}
	return strings.Replace(tag, ref, "/"+localRef, 1), nil
}

const maxMirroredAssetBytes = 100 * 1024 * 1024 // 100 MiB

func (m *externalAssetMirrorer) mirrorURL(absoluteURL, hintedExt string) (string, error) {
	if cached, ok := m.cache[absoluteURL]; ok {
		return cached, nil
	}
	if pending, ok := m.inProgress[absoluteURL]; ok {
		return pending, nil
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, absoluteURL, nil)
	if err != nil {
		return "", fmt.Errorf("creating request for external asset %s: %w", absoluteURL, err)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading external asset %s: %w", absoluteURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("downloading external asset %s: unexpected status %s", absoluteURL, resp.Status)
	}

	if resp.ContentLength > maxMirroredAssetBytes {
		return "", fmt.Errorf("external asset %s Content-Length (%d) exceeds maximum download size of %d bytes", absoluteURL, resp.ContentLength, maxMirroredAssetBytes)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMirroredAssetBytes+1))
	if err != nil {
		return "", fmt.Errorf("reading external asset %s: %w", absoluteURL, err)
	}
	if int64(len(body)) > maxMirroredAssetBytes {
		return "", fmt.Errorf("external asset %s exceeds maximum download size of %d bytes", absoluteURL, maxMirroredAssetBytes)
	}

	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	relPath := mirroredAssetRelPath(absoluteURL, contentType, hintedExt, m.assetsDir)
	m.inProgress[absoluteURL] = relPath
	defer delete(m.inProgress, absoluteURL)

	if isCSSContentType(contentType, relPath, hintedExt) {
		body, err = m.rewriteBodyAsCSS(absoluteURL, body)
		if err != nil {
			return "", err
		}
	}

	fullPath := filepath.Join(m.outDir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return "", fmt.Errorf("creating mirrored asset directory for %s: %w", absoluteURL, err)
	}
	if err := os.WriteFile(fullPath, body, 0o644); err != nil { //nolint:gosec
		return "", fmt.Errorf("writing mirrored asset %s: %w", absoluteURL, err)
	}

	m.cache[absoluteURL] = relPath
	return relPath, nil
}

func (m *externalAssetMirrorer) rewriteBodyAsCSS(absoluteURL string, body []byte) ([]byte, error) {
	baseURL, err := url.Parse(absoluteURL)
	if err != nil {
		return nil, fmt.Errorf("parsing css asset url %s: %w", absoluteURL, err)
	}
	rewritten, err := m.rewriteCSS(string(body), baseURL)
	if err != nil {
		return nil, err
	}
	return []byte(rewritten), nil
}

func (m *externalAssetMirrorer) rewriteCSS(cssText string, baseURL *url.URL) (string, error) {
	rewritten, err := rewriteCSSImports(cssText, func(ref string) (string, bool, error) {
		absoluteURL, ok := resolveExternalURL(baseURL, ref)
		if !ok {
			return ref, false, nil
		}
		localRef, err := m.mirrorURL(absoluteURL, ".css")
		if err != nil {
			return "", false, err
		}
		return "/" + localRef, true, nil
	})
	if err != nil {
		return "", err
	}

	return rewriteCSSURLs(rewritten, func(ref string) (string, bool, error) {
		absoluteURL, ok := resolveExternalURL(baseURL, ref)
		if !ok {
			return ref, false, nil
		}
		localRef, err := m.mirrorURL(absoluteURL, "")
		if err != nil {
			return "", false, err
		}
		return "/" + localRef, true, nil
	})
}

func mirrorExternalAssetsInCopiedCSS(cssRoot string, mirrorer *externalAssetMirrorer) error {
	if mirrorer == nil {
		return nil
	}
	if _, err := os.Stat(cssRoot); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	return filepath.WalkDir(cssRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.ToLower(filepath.Ext(path)) != ".css" {
			return nil
		}

		content, err := os.ReadFile(path) //nolint:gosec
		if err != nil {
			return err
		}
		rewritten, err := mirrorer.rewriteCSS(string(content), nil)
		if err != nil {
			return err
		}
		return os.WriteFile(path, []byte(rewritten), 0o644) //nolint:gosec
	})
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

func generateSitemapFromConfig(configPath, websiteRoot, outDir string) error {
	cfg, err := sitemap.LoadConfig(configPath)
	if err != nil {
		return err
	}

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

func generateSitemapFromPages(baseURL, templatesDir string, pages []sitegen.PageTemplate, outDir string, cleanURLs bool) error {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return fmt.Errorf("sitemap base URL cannot be empty")
	}

	urls := make([]sitemap.URLItem, 0, len(pages))
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

func inlineLocalAssets(doc, srcRoot string) (string, error) {
	var err error

	doc, err = inlineLocalStylesheets(doc, srcRoot)
	if err != nil {
		return "", err
	}
	doc, err = inlineLocalScripts(doc, srcRoot)
	if err != nil {
		return "", err
	}
	doc, err = inlineLocalIcons(doc, srcRoot)
	if err != nil {
		return "", err
	}
	return inlineLocalImages(doc, srcRoot)
}

func wrapInStyleTag(css, media string) string {
	if media != "" {
		return "<style media=\"" + htmlEscape(media) + "\">\n" + css + "\n</style>"
	}
	return "<style>\n" + css + "\n</style>"
}

func inlineLocalStylesheets(doc, srcRoot string) (string, error) {
	return replaceTagWith(doc, stylesheetTagRE, func(tag string, refs []string) (string, error) {
		href := refs[1]
		if isExternalRef(href) {
			return tag, nil
		}
		cssBytes, cssPath, err := readAsset(srcRoot, href)
		if err != nil {
			return "", err
		}
		inlinedCSS, err := inlineCSSURLs(string(cssBytes), srcRoot, cssPath)
		if err != nil {
			return "", err
		}
		return wrapInStyleTag(inlinedCSS, getTagAttr(tag, "media")), nil
	})
}

// inlineLocalStylesheetsPreserveURLs inlines CSS text but rewrites url() references
// to root-relative absolute paths (/dir/file.ext) instead of converting them to data
// URIs. This keeps font and image files external so they can be cached and fingerprinted
// separately, avoiding the ~280 KB of base64 font data that inlineLocalStylesheets would
// embed per page.
func inlineLocalStylesheetsPreserveURLs(doc, srcRoot string) (string, error) {
	return replaceTagWith(doc, stylesheetTagRE, func(tag string, refs []string) (string, error) {
		href := refs[1]
		if isExternalRef(href) {
			return tag, nil
		}

		cssBytes, cssPath, err := readAsset(srcRoot, href)
		if err != nil {
			return "", err
		}

		rebasedCSS, err := rewriteCSSURLs(string(cssBytes), func(ref string) (string, bool, error) {
			if isExternalRef(ref) || isDataURI(ref) {
				return ref, false, nil
			}
			// Resolve the url() ref relative to the CSS file, then make it root-relative.
			resolved := resolveCSSAssetRef(cssPath, ref)
			abs := "/" + resolved
			return abs, abs != ref, nil
		})
		if err != nil {
			return "", err
		}

		return wrapInStyleTag(rebasedCSS, getTagAttr(tag, "media")), nil
	})
}

// transformStylesheets applies position-based CSS loading: stylesheet links in <head>
// are inlined (critical path, zero-latency), while links in <body> are kept external
// and given the media="print" onload deferred-loading pattern (non-blocking).
//
// This mirrors the JS-at-end convention: document position signals loading priority.
// Templates that want a stylesheet deferred simply place its <link> in the body.
// If </head> is absent the entire document is treated as head (all CSS inlined).
func transformStylesheets(doc, assetsDir string) (string, error) {
	headEndRE := regexp.MustCompile(`(?i)</head>`)
	loc := headEndRE.FindStringIndex(doc)
	if loc == nil {
		return inlineLocalStylesheetsPreserveURLs(doc, assetsDir)
	}
	head, err := inlineLocalStylesheetsPreserveURLs(doc[:loc[0]], assetsDir)
	if err != nil {
		return "", err
	}
	tail, err := deferLocalStylesheets(doc[loc[0]:], assetsDir)
	if err != nil {
		return "", err
	}
	return head + tail, nil
}

// deferLocalStylesheets rewrites local <link rel="stylesheet"> tags found in the
// document body to use the media="print" onload deferred-loading pattern, which
// fetches the CSS without blocking rendering. A <noscript> fallback is appended after
// each transformed tag for environments where JavaScript is unavailable.
// External stylesheet URLs are left unchanged.
func deferLocalStylesheets(doc, assetsDir string) (string, error) {
	return replaceTagWith(doc, stylesheetTagRE, func(tag string, refs []string) (string, error) {
		href := refs[1]
		if isExternalRef(href) {
			return tag, nil
		}
		// Verify the asset exists; skip if not found.
		if _, _, err := readAsset(assetsDir, href); err != nil {
			return tag, nil
		}
		deferred := addOrReplaceAttr(tag, "media", "print")
		deferred = addOrReplaceAttr(deferred, "onload", "this.media='all'")
		noscript := `<noscript>` + tag + `</noscript>`
		return deferred + "\n" + noscript, nil
	})
}

// addOrReplaceAttr sets an HTML attribute value in a tag, replacing it if present.
func addOrReplaceAttr(tag, attr, value string) string {
	re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(attr) + `\s*=\s*["'][^"']*["']`)
	quoted := attr + `="` + value + `"`
	if re.MatchString(tag) {
		return re.ReplaceAllString(tag, quoted)
	}
	// Insert before the closing > of the tag.
	if idx := strings.LastIndex(tag, ">"); idx != -1 {
		return tag[:idx] + " " + quoted + tag[idx:]
	}
	return tag
}

func inlineLocalScripts(doc, srcRoot string) (string, error) {
	return replaceTagWith(doc, scriptTagRE, func(tag string, refs []string) (string, error) {
		src := refs[1]
		if isExternalRef(src) {
			return tag, nil
		}
		jsBytes, _, err := readAsset(srcRoot, src)
		if err != nil {
			return "", err
		}
		return "<script>\n" + string(jsBytes) + "\n</script>", nil
	})
}

func inlineLocalIcons(doc, srcRoot string) (string, error) {
	return replaceTagWith(doc, iconTagRE, func(tag string, refs []string) (string, error) {
		href := refs[1]
		if isExternalRef(href) {
			return tag, nil
		}
		dataURL, err := assetToDataURL(srcRoot, href)
		if err != nil {
			return "", err
		}
		return strings.Replace(tag, href, dataURL, 1), nil
	})
}

func inlineLocalImages(doc, srcRoot string) (string, error) {
	return replaceTagWith(doc, imgTagRE, func(tag string, refs []string) (string, error) {
		src := refs[1]
		if isExternalRef(src) {
			return tag, nil
		}
		dataURL, err := assetToDataURL(srcRoot, src)
		if err != nil {
			return "", err
		}
		return strings.Replace(tag, src, dataURL, 1), nil
	})
}

func replaceTagWith(doc string, re *regexp.Regexp, replacer func(tag string, refs []string) (string, error)) (string, error) {
	matches := re.FindAllStringSubmatchIndex(doc, -1)
	if len(matches) == 0 {
		return doc, nil
	}

	var out strings.Builder
	last := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		out.WriteString(doc[last:start])
		tag := doc[start:end]

		refs := make([]string, len(m)/2)
		for i := 0; i < len(m); i += 2 {
			if m[i] >= 0 && m[i+1] >= 0 {
				refs[i/2] = doc[m[i]:m[i+1]]
			}
		}

		replacement, err := replacer(tag, refs)
		if err != nil {
			return "", err
		}
		out.WriteString(replacement)
		last = end
	}
	out.WriteString(doc[last:])
	return out.String(), nil
}

func inlineCSSURLs(cssText, srcRoot, cssPath string) (string, error) {
	return rewriteCSSURLs(cssText, func(assetRef string) (string, bool, error) {
		if isExternalRef(assetRef) || strings.HasPrefix(strings.ToLower(strings.TrimSpace(assetRef)), "data:") {
			return assetRef, false, nil
		}

		resolved := resolveCSSAssetRef(cssPath, assetRef)
		dataURL, err := assetToDataURL(srcRoot, resolved)
		if err != nil {
			return "", false, err
		}
		return dataURL, true, nil
	})
}

func rewriteCSSURLs(cssText string, replacer func(ref string) (replacement string, changed bool, err error)) (string, error) {
	matches := cssURLRE.FindAllStringSubmatchIndex(cssText, -1)
	if len(matches) == 0 {
		return cssText, nil
	}

	var out strings.Builder
	last := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		urlStart, urlEnd := m[2], m[3]
		out.WriteString(cssText[last:start])

		assetRef := strings.TrimSpace(cssText[urlStart:urlEnd])
		replacement, changed, err := replacer(assetRef)
		if err != nil {
			return "", err
		}
		if !changed {
			out.WriteString(cssText[start:end])
			last = end
			continue
		}

		out.WriteString("url(\"" + replacement + "\")")
		last = end
	}
	out.WriteString(cssText[last:])
	return out.String(), nil
}

func rewriteCSSImports(cssText string, replacer func(ref string) (replacement string, changed bool, err error)) (string, error) {
	matches := cssImportRE.FindAllStringSubmatchIndex(cssText, -1)
	if len(matches) == 0 {
		return cssText, nil
	}

	var out strings.Builder
	last := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		refStart, refEnd := m[2], m[3]
		suffixStart, suffixEnd := m[4], m[5]
		out.WriteString(cssText[last:start])

		ref := strings.TrimSpace(cssText[refStart:refEnd])
		replacement, changed, err := replacer(ref)
		if err != nil {
			return "", err
		}
		if !changed {
			out.WriteString(cssText[start:end])
			last = end
			continue
		}

		suffix := ""
		if suffixStart >= 0 && suffixEnd >= 0 {
			suffix = cssText[suffixStart:suffixEnd]
		}
		out.WriteString("@import url(\"" + replacement + "\")" + suffix + ";")
		last = end
	}
	out.WriteString(cssText[last:])
	return out.String(), nil
}

func resolveCSSAssetRef(cssPath, ref string) string {
	if strings.HasPrefix(ref, "/") {
		return strings.TrimPrefix(ref, "/")
	}
	base := filepath.Dir(cssPath)
	return filepath.ToSlash(filepath.Clean(filepath.Join(base, ref)))
}

func assetToDataURL(srcRoot, ref string) (string, error) {
	content, path, err := readAsset(srcRoot, ref)
	if err != nil {
		return "", err
	}
	mimeType := detectMimeType(path, content)
	return "data:" + mimeType + ";base64," + encodeBase64(content), nil
}

func readAsset(srcRoot, ref string) ([]byte, string, error) {
	cleanRef := strings.TrimPrefix(ref, "/")
	fullPath := filepath.Clean(filepath.Join(srcRoot, filepath.FromSlash(cleanRef)))
	if !strings.HasPrefix(fullPath, filepath.Clean(srcRoot)+string(filepath.Separator)) && fullPath != filepath.Clean(srcRoot) {
		return nil, "", fmt.Errorf("asset path escapes source root: %s", ref)
	}
	realPath, err := filepath.EvalSymlinks(fullPath)
	if err != nil {
		return nil, "", fmt.Errorf("resolving asset path %s: %w", ref, err)
	}
	realRoot, err := filepath.EvalSymlinks(filepath.Clean(srcRoot))
	if err != nil {
		return nil, "", fmt.Errorf("resolving source root: %w", err)
	}
	if !strings.HasPrefix(realPath, realRoot+string(filepath.Separator)) && realPath != realRoot {
		return nil, "", fmt.Errorf("asset path escapes source root via symlink: %s", ref)
	}
	content, err := os.ReadFile(realPath)
	if err != nil {
		return nil, "", fmt.Errorf("reading asset %s: %w", ref, err)
	}
	return content, cleanRef, nil
}

func detectMimeType(path string, content []byte) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".css":
		return mimeTextCSS
	case ".js":
		return "application/javascript"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".woff":
		return "font/woff"
	case extWoff2:
		return "font/woff2"
	case ".ttf":
		return "font/ttf"
	case ".ico":
		return "image/x-icon"
	default:
		return http.DetectContentType(content)
	}
}

func isExternalRef(ref string) bool {
	lower := strings.ToLower(strings.TrimSpace(ref))
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "//")
}

func resolveExternalURL(baseURL *url.URL, ref string) (string, bool) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return "", false
	}

	if strings.HasPrefix(trimmed, "//") {
		return resolveSchemeRelativeURL(baseURL, trimmed), true
	}

	if shouldSkipExternalURL(trimmed) {
		return "", false
	}
	if absoluteURL, ok := parseAbsoluteHTTPURL(trimmed); ok {
		return absoluteURL, true
	}
	if baseURL == nil {
		return "", false
	}

	return resolveRelativeHTTPURL(baseURL, trimmed)
}

func shouldSkipExternalURL(trimmedRef string) bool {
	lower := strings.ToLower(trimmedRef)
	switch {
	case strings.HasPrefix(lower, "data:"),
		strings.HasPrefix(lower, "mailto:"),
		strings.HasPrefix(lower, "tel:"),
		strings.HasPrefix(lower, "javascript:"),
		strings.HasPrefix(lower, "#"):
		return true
	default:
		return false
	}
}

func resolveSchemeRelativeURL(baseURL *url.URL, schemeRelative string) string {
	scheme := "https"
	if baseURL != nil && baseURL.Scheme != "" {
		scheme = baseURL.Scheme
	}
	return scheme + ":" + schemeRelative
}

func parseAbsoluteHTTPURL(ref string) (string, bool) {
	parsed, err := url.Parse(ref)
	if err != nil {
		return "", false
	}
	if !parsed.IsAbs() {
		return "", false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false
	}
	return parsed.String(), true
}

func resolveRelativeHTTPURL(baseURL *url.URL, ref string) (string, bool) {
	resolved := baseURL.ResolveReference(&url.URL{Path: ref})
	if strings.Contains(ref, "?") || strings.Contains(ref, "#") {
		if parsed, err := url.Parse(ref); err == nil {
			resolved = baseURL.ResolveReference(parsed)
		}
	}
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return "", false
	}
	if resolved.Host == "" {
		return "", false
	}
	return resolved.String(), true
}

func hintedExtFromPreload(tag string) string {
	switch strings.ToLower(strings.TrimSpace(getTagAttr(tag, "as"))) {
	case "style":
		return ".css"
	case "script":
		return ".js"
	case "font":
		if hrefType := strings.ToLower(strings.TrimSpace(getTagAttr(tag, "type"))); hrefType != "" {
			return extensionFromContentType(hrefType)
		}
		return extWoff2
	case "image":
		return ""
	default:
		return ""
	}
}

func mirroredAssetRelPath(absoluteURL, contentType, hintedExt, assetsDir string) string {
	parsed, err := url.Parse(absoluteURL)
	if err != nil {
		sum := sha256.Sum256([]byte(absoluteURL))
		return path.Join(assetsDir, "unknown", hex.EncodeToString(sum[:8])+normalizeExt(hintedExt))
	}

	host := sanitizePathSegment(parsed.Host)
	segments := []string{}
	cleanPath := strings.Trim(parsed.Path, "/")
	if cleanPath != "" {
		for _, segment := range strings.Split(cleanPath, "/") {
			sanitized := sanitizePathSegment(segment)
			if sanitized != "" {
				segments = append(segments, sanitized)
			}
		}
	}
	if len(segments) == 0 {
		segments = []string{"index"}
	}

	fileName := segments[len(segments)-1]
	dirParts := segments[:len(segments)-1]
	ext := normalizeExt(filepath.Ext(fileName))
	if ext == "" {
		ext = normalizeExt(hintedExt)
	}
	if ext == "" {
		ext = extensionFromContentType(contentType)
	}
	if ext == "" {
		ext = ".bin"
	}

	fileStem := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	if fileStem == "" {
		fileStem = "index"
	}
	if parsed.RawQuery != "" {
		fileStem += "--" + shortHash(parsed.RawQuery)
	}

	parts := []string{assetsDir, host}
	parts = append(parts, dirParts...)
	parts = append(parts, fileStem+ext)
	return path.Join(parts...)
}

func sanitizePathSegment(v string) string {
	if v == "" {
		return ""
	}
	replacer := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "_",
		"?", "_",
		"&", "_",
		"=", "_",
		"%", "_",
		"+", "_",
	)
	v = replacer.Replace(v)
	v = strings.Trim(v, "._-")
	if v == "" {
		return "asset"
	}
	return v
}

func shortHash(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:6])
}

func normalizeExt(v string) string {
	if v == "" {
		return ""
	}
	if strings.HasPrefix(v, ".") {
		return strings.ToLower(v)
	}
	return "." + strings.ToLower(v)
}

func extensionFromContentType(contentType string) string {
	trimmed := strings.TrimSpace(strings.Split(contentType, ";")[0])
	if trimmed == "" {
		return ""
	}
	if exts, err := mime.ExtensionsByType(trimmed); err == nil {
		for _, ext := range exts {
			normalized := normalizeExt(ext)
			if normalized != "" {
				return normalized
			}
		}
	}
	switch trimmed {
	case mimeTextCSS:
		return ".css"
	case "application/javascript", "text/javascript":
		return ".js"
	case "font/woff2":
		return extWoff2
	case "font/woff":
		return ".woff"
	case "font/ttf", "application/x-font-ttf":
		return ".ttf"
	case "image/svg+xml":
		return ".svg"
	case "image/x-icon":
		return ".ico"
	default:
		return ""
	}
}

func isCSSContentType(contentType, relPath, hintedExt string) bool {
	if strings.EqualFold(strings.TrimSpace(strings.Split(contentType, ";")[0]), mimeTextCSS) {
		return true
	}
	switch normalizeExt(filepath.Ext(relPath)) {
	case ".css":
		return true
	}
	return normalizeExt(hintedExt) == ".css"
}

func htmlEscape(v string) string {
	v = strings.ReplaceAll(v, "&", "&amp;")
	v = strings.ReplaceAll(v, "\"", "&quot;")
	v = strings.ReplaceAll(v, "<", "&lt;")
	v = strings.ReplaceAll(v, ">", "&gt;")
	return v
}

func encodeBase64(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

func getTagAttr(tag, attr string) string {
	re := regexp.MustCompile(`(?i)` + attr + `\s*=\s*["']([^"']+)["']`)
	m := re.FindStringSubmatch(tag)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func copyStaticAssets(srcRoot, dstRoot string) error {
	dirs := []string{"css", "fonts", "images", "js", "ld"}
	files := []string{"favicon.ico", "send.js", "contactScript.js", "robots.txt", sitemapXML}

	if err := copyExistingDirs(srcRoot, dstRoot, dirs); err != nil {
		return err
	}
	return copyExistingFiles(srcRoot, dstRoot, files)
}

func copyExistingDirs(srcRoot, dstRoot string, dirs []string) error {
	for _, dir := range dirs {
		src := filepath.Join(srcRoot, dir)
		if _, err := os.Stat(src); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}

		dst := filepath.Join(dstRoot, dir)
		if err := copyDir(src, dst); err != nil {
			return err
		}
	}
	return nil
}

func copyExistingFiles(srcRoot, dstRoot string, files []string) error {
	for _, file := range files {
		src := filepath.Join(srcRoot, file)
		if _, err := os.Stat(src); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}

		dst := filepath.Join(dstRoot, file)
		if err := copyFile(src, dst); err != nil {
			return err
		}
	}
	return nil
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
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
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	out, err := os.Create(dst)
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}

	return out.Close()
}

// filterInternalPages removes pages marked internal: true in the site data
// (pages.<name>.internal). Such pages are still rendered (for asset usage
// validation) but excluded from HTML output and sitemap generation.
func filterInternalPages(pages []sitegen.PageTemplate, siteData map[string]any) []sitegen.PageTemplate {
	pagesData, _ := siteData["pages"].(map[string]any)
	result := pages[:0:0]
	for _, p := range pages {
		pd, _ := pagesData[p.Name].(map[string]any)
		if internal, _ := pd["internal"].(bool); !internal {
			result = append(result, p)
		}
	}
	return result
}
