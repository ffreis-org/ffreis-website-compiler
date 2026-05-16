package cmdutil

import (
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
