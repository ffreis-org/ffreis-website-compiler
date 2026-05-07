package cmdutil

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"ffreis-website-compiler/internal/sitegen"
)

func ResolveWebsitePaths(websiteRoot string) (string, string, error) {
	newAssets := filepath.Join(websiteRoot, "src", "assets")
	newTemplates := filepath.Join(websiteRoot, "src", "templates")
	if DirExists(newAssets) && DirExists(newTemplates) {
		return newAssets, newTemplates, nil
	}

	legacyAssets := filepath.Join(websiteRoot, "site")
	legacyTemplates := filepath.Join(websiteRoot, "templates")
	if DirExists(legacyAssets) && DirExists(legacyTemplates) {
		return legacyAssets, legacyTemplates, nil
	}

	return "", "", fmt.Errorf(
		"could not resolve website directories under %s; expected src/assets + src/templates (or legacy site + templates)",
		websiteRoot,
	)
}

func DirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func FileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func LogSiteDataOverride(logger *slog.Logger, siteDataResult sitegen.SiteDataLoadResult) {
	if !siteDataResult.UsedOverride || !siteDataResult.DefaultPathFound {
		return
	}
	logger.Warn(
		"site data override supersedes local site data file",
		"override_source", siteDataResult.Source,
		"local_site_data", siteDataResult.DefaultPath,
		"site_data_layers", siteDataResult.Layers,
	)
}

func ValidateSiteDataAndUsage(pages []sitegen.PageTemplate, siteDataResult sitegen.SiteDataLoadResult, siteDataContractResult sitegen.SiteDataContractLoadResult) error {
	if err := sitegen.ValidateSiteData(siteDataResult.Data, siteDataContractResult.Contract); err != nil {
		return fmt.Errorf("validating site data against contract: %w", err)
	}

	contract := siteDataContractResult.Contract
	if len(contract.Required) == 0 && len(contract.Allowed) == 0 {
		return nil
	}

	usedPaths, err := sitegen.TraceSiteDataUsage(pages, siteDataResult.Data)
	if err != nil {
		return fmt.Errorf("tracing site data usage: %w", err)
	}
	if err := sitegen.ValidateSiteDataContractUsage(contract, usedPaths); err != nil {
		return fmt.Errorf("validating site data contract usage: %w", err)
	}
	return nil
}

func ResolveTemplatesRoot(websiteRoot string) (string, error) {
	newTemplates := filepath.Join(websiteRoot, "src", "templates")
	if DirExists(newTemplates) {
		return newTemplates, nil
	}

	legacyTemplates := filepath.Join(websiteRoot, "templates")
	if DirExists(legacyTemplates) {
		return legacyTemplates, nil
	}

	return "", fmt.Errorf(
		"could not resolve templates directory under %s; expected src/templates (or legacy templates)",
		websiteRoot,
	)
}

// ValidateSiteDataAndUsageFromRoot is like ValidateSiteDataAndUsage but loads
// page templates lazily from templatesRoot (only when the contract has required/allowed keys).
func ValidateSiteDataAndUsageFromRoot(templatesRoot string, siteDataResult sitegen.SiteDataLoadResult, siteDataContractResult sitegen.SiteDataContractLoadResult) error {
	if err := sitegen.ValidateSiteData(siteDataResult.Data, siteDataContractResult.Contract); err != nil {
		return fmt.Errorf("validating site data against contract: %w", err)
	}

	contract := siteDataContractResult.Contract
	if len(contract.Required) == 0 && len(contract.Allowed) == 0 {
		return nil
	}

	pages, err := sitegen.LoadPageTemplatesFromRoot(templatesRoot)
	if err != nil {
		return fmt.Errorf("loading templates for site data usage validation: %w", err)
	}
	usedPaths, err := sitegen.TraceSiteDataUsage(pages, siteDataResult.Data)
	if err != nil {
		return fmt.Errorf("tracing site data usage: %w", err)
	}
	if err := sitegen.ValidateSiteDataContractUsage(contract, usedPaths); err != nil {
		return fmt.Errorf("validating site data contract usage: %w", err)
	}
	return nil
}

func LoadAndValidateSiteData(logger *slog.Logger, templatesDir, siteDataSource string) ([]sitegen.PageTemplate, sitegen.SiteDataLoadResult, sitegen.SiteDataContractLoadResult, error) {
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
	LogSiteDataOverride(logger, siteDataResult)
	if err := ValidateSiteDataAndUsage(pages, siteDataResult, siteDataContractResult); err != nil {
		return nil, sitegen.SiteDataLoadResult{}, sitegen.SiteDataContractLoadResult{}, err
	}
	return pages, siteDataResult, siteDataContractResult, nil
}

const recaptchaTestSiteKey = "6LeIxAcTAAAAAJcZVRqyHh71UMIEGNQ_MXjiZKhI"

func RenderPages(pages []sitegen.PageTemplate, siteData map[string]any) (map[string]string, error) {
	// Inject the official Google test key when recaptcha_site_key is absent.
	// Production site.yaml sets the real key; local dev and builds without a key
	// get the test key automatically so reCAPTCHA loads on any domain.
	if _, ok := siteData["recaptcha_site_key"]; !ok {
		siteData["recaptcha_site_key"] = recaptchaTestSiteKey
	}
	renderedPages := make(map[string]string, len(pages))
	for _, page := range pages {
		var rendered bytes.Buffer
		if err := page.Tmpl.ExecuteTemplate(&rendered, "layout", sitegen.NewTemplateData(page.Name, siteData)); err != nil {
			return nil, fmt.Errorf("rendering %s.html: %w", page.Name, err)
		}
		renderedPages[page.Name] = rendered.String()
	}
	return renderedPages, nil
}
