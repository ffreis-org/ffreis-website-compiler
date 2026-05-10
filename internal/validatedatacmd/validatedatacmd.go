package validatedatacmd

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"ffreis-website-compiler/internal/cmdutil"
	"ffreis-website-compiler/internal/sitegen"
)

func Run(args []string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}

	opts, err := parseValidateDataOptions(args)
	if err != nil {
		return err
	}

	templatesRoot, err := resolveTemplatesRootFlag(opts.websiteRoot, opts.templatesDir)
	if err != nil {
		return err
	}

	siteDataResult, siteDataContractResult, err := loadAndValidateSiteData(logger, templatesRoot, opts.siteDataSource)
	if err != nil {
		return err
	}

	logger.Info(
		"site data validation passed",
		"website_root", opts.websiteRoot,
		"templates_dir", templatesRoot,
		"site_data_source", cmdutil.FirstNonEmpty(siteDataResult.Source, siteDataResult.DefaultPath),
		"site_data_layers", siteDataResult.Layers,
		"site_data_contract_source", cmdutil.FirstNonEmpty(siteDataContractResult.Source, siteDataContractResult.DefaultPath),
	)
	return nil
}

type validateDataOptions struct {
	websiteRoot    string
	templatesDir   string
	siteDataSource string
}

func parseValidateDataOptions(args []string) (validateDataOptions, error) {
	fs := flag.NewFlagSet("validate-site-data", flag.ContinueOnError)
	var opts validateDataOptions
	fs.StringVar(&opts.websiteRoot, "website-root", ".", "website project root; expects <website-root>/src/{assets,templates} (legacy fallback: <website-root>/{site,templates})")
	fs.StringVar(&opts.templatesDir, "templates-dir", "", "path to templates root folder (defaults to <website-root>/src/templates, then <website-root>/templates)")
	fs.StringVar(&opts.siteDataSource, "site-data", "", "optional site data source override; supports file/URL sources or a directory containing YAML layers")
	if err := fs.Parse(args); err != nil {
		return validateDataOptions{}, err
	}
	return opts, nil
}

func resolveTemplatesRootFlag(websiteRoot, templatesDirFlag string) (string, error) {
	templatesRoot := strings.TrimSpace(templatesDirFlag)
	if templatesRoot != "" {
		return templatesRoot, nil
	}
	return cmdutil.ResolveTemplatesRoot(websiteRoot)
}

func loadAndValidateSiteData(logger *slog.Logger, templatesRoot, siteDataSource string) (sitegen.SiteDataLoadResult, sitegen.SiteDataContractLoadResult, error) {
	siteDataResult, err := sitegen.LoadSiteData(templatesRoot, siteDataSource)
	if err != nil {
		return sitegen.SiteDataLoadResult{}, sitegen.SiteDataContractLoadResult{}, fmt.Errorf("loading site data: %w", err)
	}
	siteDataContractResult, err := sitegen.LoadSiteDataContract(templatesRoot)
	if err != nil {
		return sitegen.SiteDataLoadResult{}, sitegen.SiteDataContractLoadResult{}, fmt.Errorf("loading site data contract: %w", err)
	}

	cmdutil.LogSiteDataOverride(logger, siteDataResult)

	if err := sitegen.ValidateSiteDataAndUsageFromRoot(templatesRoot, siteDataResult, siteDataContractResult); err != nil {
		return sitegen.SiteDataLoadResult{}, sitegen.SiteDataContractLoadResult{}, err
	}

	return siteDataResult, siteDataContractResult, nil
}
