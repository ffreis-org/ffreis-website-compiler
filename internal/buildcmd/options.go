package buildcmd

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"ffreis-website-compiler/internal/cmdutil"
)

type buildOptions struct {
	websiteRoot             string
	assetsDir               string
	templatesDir            string
	sitemapConfig           string
	sitemapBaseURL          string
	siteDataSource          string
	outDir                  string
	postsDir                string
	projectsFile            string
	coursesFile             string
	itemsPerPage            int
	copyAssets              bool
	inlineAssets            bool
	jsInlineThreshold       int
	jsSharedInlineThreshold int             // -1 = disabled; use jsInlineThreshold for all scripts
	sharedScripts           map[string]bool // populated at runtime by collectSharedScripts
	embedFonts              bool
	inlineBodyCSS           bool
	rasterInlineThreshold   int
	siblingBasePaths        []string
	mirrorExternalAssets    bool
	mirroredAssetsDir       string
	enableSanity            bool
	strictContract          bool
	cleanURLs               bool
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
	fs.IntVar(&opts.jsInlineThreshold, "js-inline-threshold", defaultJSInlineThreshold, "inline local <script src> files smaller than this many bytes as <script> blocks; 0 to disable")
	fs.IntVar(&opts.jsSharedInlineThreshold, "js-shared-inline-threshold", -1, "for JS files referenced by more than one page, only inline if smaller than this many bytes; -1 to disable (all JS uses -js-inline-threshold regardless of page count)")
	fs.BoolVar(&opts.embedFonts, "embed-fonts", false, "embed font files (woff2/woff/ttf/otf/eot) as base64 data URIs in inlined CSS; eliminates font files from dist but increases HTML size")
	fs.BoolVar(&opts.inlineBodyCSS, "inline-body-css", false, "inline body <link rel=stylesheet> as <style> blocks instead of deferred external links; eliminates CSS files from dist but prevents cross-page CSS cache reuse")
	fs.IntVar(&opts.rasterInlineThreshold, "raster-inline-threshold", 0, "inline local raster <img> files smaller than this many bytes as base64 data URIs; 0 to disable; skips LQIP-processed images and SVGs")
	var siblingBasePathsFlag string
	fs.StringVar(&siblingBasePathsFlag, "sibling-base-paths", "", "comma-separated URL prefixes of sibling deployments sharing the same CloudFront distribution (e.g. 'en,jp'); links under these prefixes are skipped by the internal link checker")
	fs.BoolVar(&opts.mirrorExternalAssets, "mirror-external-assets", false, "download external css/js/image/font assets into output and rewrite references to local copies")
	fs.StringVar(&opts.mirroredAssetsDir, "mirrored-assets-dir", "external", "subdirectory inside output for mirrored external assets")
	fs.BoolVar(&opts.enableSanity, "sanity", true, "fail the build if generic sanity checks fail (site contract + invariants + asset reachability)")
	fs.BoolVar(&opts.strictContract, "strict-contract", true, "fail if any allowed contract path is not referenced by any template (disable for local dev with in-progress templates)")
	fs.BoolVar(&opts.cleanURLs, "clean-urls", false, "output each page as <name>/index.html instead of <name>.html for extension-free URLs; updates sitemap accordingly")
	fs.StringVar(&opts.postsDir, "posts-dir", "", "path to blog posts directory (posts/<slug>/index.md layout); enables Markdown blog post generation and RSS feed when set")
	fs.StringVar(&opts.projectsFile, "projects-file", "", "path to projects.yaml (ffreis-projects repo); enables /projects/ paginated page generation when set")
	fs.StringVar(&opts.coursesFile, "courses-file", "", "path to courses.yaml (ffreis-courses repo); enables /courses/ paginated page generation when set")
	fs.IntVar(&opts.itemsPerPage, "items-per-page", 12, "number of items per paginated page for projects, courses, and blog")

	if err := fs.Parse(args); err != nil {
		return buildOptions{}, err
	}

	if assetsDirFlag == "" && siteDirFlag != "" {
		assetsDirFlag = siteDirFlag
	}
	opts.assetsDir = assetsDirFlag
	opts.templatesDir = templatesDirFlag

	if siblingBasePathsFlag != "" {
		for _, s := range strings.Split(siblingBasePathsFlag, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				opts.siblingBasePaths = append(opts.siblingBasePaths, s)
			}
		}
	}

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
	// External url() refs inside inline <style> blocks are handled by mirrorer.rewriteHTML,
	// which runs per-page in transformPage. No pre-pass over copied CSS files is needed
	// because css/ originals are no longer copied to the output directory.
	return newExternalAssetMirrorer(opts.outDir, opts.mirroredAssetsDir), nil
}
