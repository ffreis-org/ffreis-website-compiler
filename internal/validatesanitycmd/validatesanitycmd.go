package validatesanitycmd

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"ffreis-website-compiler/internal/assetusage"
	"ffreis-website-compiler/internal/cmdutil"
	"ffreis-website-compiler/internal/sitegen"
)

func Run(args []string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}

	opts, err := parseValidateSanityOptions(args)
	if err != nil {
		return err
	}

	assetsDir, templatesRoot, sanityDir, sanityConfig, sanityConfigSource, err := resolveValidateSanityPaths(opts)
	if err != nil {
		return err
	}

	pages, siteDataResult, siteDataContractResult, err := loadAndValidateSiteData(logger, templatesRoot, opts.siteDataSource, sanityConfig)
	if err != nil {
		return err
	}

	if err := maybeRunSanityDirChecks(context.Background(), opts, sanityDir, logger); err != nil {
		return err
	}
	if err := maybeValidateAssets(opts, assetsDir, pages, siteDataResult.Data); err != nil {
		return err
	}

	logger.Info(
		"sanity validation passed",
		"website_root", opts.websiteRoot,
		"assets_dir", assetsDir,
		"templates_dir", templatesRoot,
		"site_data_source", cmdutil.FirstNonEmpty(siteDataResult.Source, siteDataResult.DefaultPath),
		"site_data_layers", siteDataResult.Layers,
		"site_data_contract_source", cmdutil.FirstNonEmpty(siteDataContractResult.Source, siteDataContractResult.DefaultPath),
		"sanity_dir", sanityDir,
		"sanity_config_source", sanityConfigSource,
		"ran_sanity_dir_checks", opts.runSanityDirChecks,
		"checked_assets", opts.checkAssets,
	)
	return nil
}

type validateSanityOptions struct {
	websiteRoot         string
	assetsDir           string
	templatesDir        string
	siteDataSource      string
	sanityDir           string
	runSanityDirChecks  bool
	sanityChecksDirName string
	checkAssets         bool
}

func parseValidateSanityOptions(args []string) (validateSanityOptions, error) {
	fs := flag.NewFlagSet("validate-sanity", flag.ContinueOnError)

	var opts validateSanityOptions
	fs.StringVar(&opts.websiteRoot, "website-root", ".", "website project root; expects <website-root>/src/{assets,templates} (legacy fallback: <website-root>/{site,templates})")
	fs.StringVar(&opts.assetsDir, "assets-dir", "", "path to source assets folder (defaults to <website-root>/src/assets, then <website-root>/site)")
	fs.StringVar(&opts.templatesDir, "templates-dir", "", "path to templates root folder (defaults to <website-root>/src/templates, then <website-root>/templates)")
	fs.StringVar(&opts.siteDataSource, "site-data", "", "optional site data source override; supports file/URL sources or a directory containing YAML layers")
	fs.StringVar(&opts.sanityDir, "sanity-dir", "", "optional sanity folder (defaults to <website-root>/sanity if it exists); can contain sanity.yaml to enable/disable checks")
	fs.BoolVar(&opts.runSanityDirChecks, "run-sanity-dir-checks", true, "run executable checks from <sanity-dir>/checks.d/ (if present)")
	fs.StringVar(&opts.sanityChecksDirName, "sanity-checks-dir", "checks.d", "relative directory name under sanity dir that contains executable sanity checks")
	fs.BoolVar(&opts.checkAssets, "check-assets", true, "also validate rendered pages only reference local css/js assets (same behavior as validate-assets)")

	if err := fs.Parse(args); err != nil {
		return validateSanityOptions{}, err
	}

	opts.assetsDir = strings.TrimSpace(opts.assetsDir)
	opts.templatesDir = strings.TrimSpace(opts.templatesDir)
	opts.sanityDir = strings.TrimSpace(opts.sanityDir)
	return opts, nil
}

func resolveValidateSanityPaths(opts validateSanityOptions) (assetsDir, templatesRoot, sanityDir string, sanityConfig sitegen.SanityConfig, sanityConfigSource string, err error) {
	assetsDir = opts.assetsDir
	templatesRoot = opts.templatesDir
	if assetsDir == "" || templatesRoot == "" {
		resolvedAssetsDir, resolvedTemplatesDir, err := cmdutil.ResolveWebsitePaths(opts.websiteRoot)
		if err != nil {
			return "", "", "", sitegen.SanityConfig{}, "", err
		}
		if assetsDir == "" {
			assetsDir = resolvedAssetsDir
		}
		if templatesRoot == "" {
			templatesRoot = resolvedTemplatesDir
		}
	}

	sanityDir = opts.sanityDir
	if sanityDir == "" {
		defaultSanityDir := filepath.Join(opts.websiteRoot, "sanity")
		if cmdutil.DirExists(defaultSanityDir) {
			sanityDir = defaultSanityDir
		}
	}
	sanityConfig, sanityConfigSource, err = loadSanityConfig(sanityDir)
	if err != nil {
		return "", "", "", sitegen.SanityConfig{}, "", fmt.Errorf("loading sanity config: %w", err)
	}
	return assetsDir, templatesRoot, sanityDir, sanityConfig, sanityConfigSource, nil
}

func loadAndValidateSiteData(logger *slog.Logger, templatesRoot, siteDataSource string, sanityConfig sitegen.SanityConfig) ([]sitegen.PageTemplate, sitegen.SiteDataLoadResult, sitegen.SiteDataContractLoadResult, error) {
	pages, err := sitegen.LoadPageTemplatesFromRoot(templatesRoot)
	if err != nil {
		return nil, sitegen.SiteDataLoadResult{}, sitegen.SiteDataContractLoadResult{}, fmt.Errorf("loading templates: %w", err)
	}
	siteDataResult, err := sitegen.LoadSiteData(templatesRoot, siteDataSource)
	if err != nil {
		return nil, sitegen.SiteDataLoadResult{}, sitegen.SiteDataContractLoadResult{}, fmt.Errorf("loading site data: %w", err)
	}
	siteDataContractResult, err := sitegen.LoadSiteDataContract(templatesRoot)
	if err != nil {
		return nil, sitegen.SiteDataLoadResult{}, sitegen.SiteDataContractLoadResult{}, fmt.Errorf("loading site data contract: %w", err)
	}

	cmdutil.LogSiteDataOverride(logger, siteDataResult)
	if err := sitegen.ValidateSiteDataAndUsage(pages, siteDataResult, siteDataContractResult); err != nil {
		return nil, sitegen.SiteDataLoadResult{}, sitegen.SiteDataContractLoadResult{}, err
	}

	if err := sitegen.ValidateSiteSanity(siteDataResult.Data, sanityConfig); err != nil {
		return nil, sitegen.SiteDataLoadResult{}, sitegen.SiteDataContractLoadResult{}, fmt.Errorf("validating site sanity rules: %w", err)
	}

	return pages, siteDataResult, siteDataContractResult, nil
}

func maybeRunSanityDirChecks(ctx context.Context, opts validateSanityOptions, sanityDir string, logger *slog.Logger) error {
	if !opts.runSanityDirChecks {
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving compiler executable: %w", err)
	}
	return runSanityChecksFromDir(ctx, opts.websiteRoot, sanityDir, opts.sanityChecksDirName, executable, logger)
}

func maybeValidateAssets(opts validateSanityOptions, assetsDir string, pages []sitegen.PageTemplate, siteData map[string]any) error {
	if !opts.checkAssets {
		return nil
	}

	renderedPages, err := sitegen.RenderPages(pages, siteData)
	if err != nil {
		return err
	}
	if _, err := assetusage.Validate(assetsDir, renderedPages); err != nil {
		return fmt.Errorf("validating local css/js asset usage: %w", err)
	}
	return nil
}

type sanityConfigFile struct {
	Version int `yaml:"version"`
	Checks  struct {
		CourseStartMatchesFirstSession         *bool `yaml:"course_start_matches_first_session"`
		CourseDurationHoursMatchesSessionHours *bool `yaml:"course_duration_hours_matches_session_hours"`
	} `yaml:"checks"`
}

func loadSanityConfig(sanityDir string) (sitegen.SanityConfig, string, error) {
	config := sitegen.DefaultSanityConfig()
	if strings.TrimSpace(sanityDir) == "" {
		return config, "", nil
	}

	candidates := []string{
		filepath.Join(sanityDir, "sanity.yaml"),
		filepath.Join(sanityDir, "sanity.yml"),
	}
	var path string
	for _, candidate := range candidates {
		if cmdutil.FileExists(candidate) {
			path = candidate
			break
		}
	}
	if path == "" {
		return config, "", nil
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return sitegen.SanityConfig{}, "", err
	}

	var parsed sanityConfigFile
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		return sitegen.SanityConfig{}, "", err
	}

	if parsed.Checks.CourseStartMatchesFirstSession != nil {
		config.CourseStartMatchesFirstSession = *parsed.Checks.CourseStartMatchesFirstSession
	}
	if parsed.Checks.CourseDurationHoursMatchesSessionHours != nil {
		config.CourseDurationHoursMatchesSessionHours = *parsed.Checks.CourseDurationHoursMatchesSessionHours
	}
	return config, path, nil
}

func runSanityChecksFromDir(ctx context.Context, websiteRoot, sanityDir, sanityChecksDirName, compilerExe string, logger *slog.Logger) error {
	if strings.TrimSpace(sanityDir) == "" {
		return nil
	}
	checksRoot := filepath.Join(sanityDir, sanityChecksDirName)
	if !cmdutil.DirExists(checksRoot) {
		return nil
	}

	entries, err := readSortedDirEntries(checksRoot)
	if err != nil {
		return err
	}

	ran := 0
	for _, entry := range entries {
		checkPath, runnable, err := resolveRunnableSanityCheck(checksRoot, entry)
		if err != nil {
			return err
		}
		if !runnable {
			continue
		}

		ran++
		if err := runSanityCheck(ctx, websiteRoot, sanityDir, checksRoot, compilerExe, checkPath, logger); err != nil {
			return err
		}
	}

	if ran == 0 {
		logger.Info("no executable sanity checks found", "checks_dir", checksRoot)
	}
	return nil
}

func readSortedDirEntries(dir string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading sanity checks directory %s: %w", dir, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

func resolveRunnableSanityCheck(checksRoot string, entry os.DirEntry) (string, bool, error) {
	if entry.IsDir() {
		return "", false, nil
	}

	checkPath := filepath.Join(checksRoot, entry.Name())
	info, err := os.Stat(checkPath)
	if err != nil {
		return "", false, fmt.Errorf("stat sanity check %s: %w", checkPath, err)
	}
	if !isRunnableSanityCheckFile(entry.Name(), info.Mode()) {
		return "", false, nil
	}
	return checkPath, true, nil
}

func runSanityCheck(ctx context.Context, websiteRoot, sanityDir, checksRoot, compilerExe, checkPath string, logger *slog.Logger) error {
	logger.Info("running sanity check", "check", checkPath)

	cmd, err := sanityCheckCommand(ctx, checkPath)
	if err != nil {
		return err
	}
	cmd.Dir = websiteRoot
	cmd.Env = append(os.Environ(),
		"WEBSITE_ROOT="+websiteRoot,
		"SANITY_DIR="+sanityDir,
		"SANITY_CHECKS_DIR="+checksRoot,
		"WEBSITE_COMPILER_EXE="+compilerExe,
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("sanity check failed (%s): %w\n%s", checkPath, err, strings.TrimSpace(string(out)))
	}
	if len(out) > 0 {
		logger.Info("sanity check output", "check", checkPath, "output", strings.TrimSpace(string(out)))
	}
	return nil
}

func isRunnableSanityCheckFile(name string, mode os.FileMode) bool {
	if mode&0o111 != 0 {
		return true
	}
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".sh", ".bash", ".py", ".rb":
		return true
	default:
		return false
	}
}

func sanityCheckCommand(ctx context.Context, path string) (*exec.Cmd, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".sh" || ext == ".bash" {
		return exec.CommandContext(ctx, "/bin/bash", path), nil
	}
	if ext == ".py" {
		py, err := exec.LookPath("python3")
		if err != nil {
			return nil, fmt.Errorf("python3 not found in PATH: %w", err)
		}
		return exec.CommandContext(ctx, py, path), nil
	}
	if ext == ".rb" {
		rb, err := exec.LookPath("ruby")
		if err != nil {
			return nil, fmt.Errorf("ruby not found in PATH: %w", err)
		}
		return exec.CommandContext(ctx, rb, path), nil
	}
	if runtime.GOOS == "windows" && ext == ".cmd" {
		cmd, err := exec.LookPath("cmd.exe")
		if err != nil {
			return nil, fmt.Errorf("cmd.exe not found in PATH: %w", err)
		}
		return exec.CommandContext(ctx, cmd, "/c", path), nil
	}
	return exec.CommandContext(ctx, path), nil
}
