package sitegen

import (
	"bytes"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PageTemplate associates a page name (without extension) to its parsed template.
type PageTemplate struct {
	Name string
	Tmpl *template.Template
}

type SiteDataLoadResult struct {
	Data             map[string]any
	Source           string
	Layers           []string
	DefaultPath      string
	UsedOverride     bool
	DefaultPathFound bool
}

type SiteDataContract struct {
	Required []string `yaml:"required"`
	Allowed  []string `yaml:"allowed"`
	// CompilerConsumed lists paths that are read by the compiler's Go code rather
	// than by templates. They must be present in data (not dangling) and templates
	// may optionally access them, but the strict-contract check never requires
	// templates to use them.
	CompilerConsumed []string `yaml:"compiler_consumed"`
}

type SiteDataContractLoadResult struct {
	Contract    SiteDataContract
	Source      string
	DefaultPath string
}

// LoadPageTemplatesFromRoot parses the shared layout/partials plus each page template.
// templatesRoot is expected to contain: layout/, partials/, pages/.
func LoadPageTemplatesFromRoot(templatesRoot string) ([]PageTemplate, error) {
	files, err := filepath.Glob(filepath.Join(templatesRoot, "pages", "*.gohtml"))
	if err != nil {
		return nil, err
	}
	partials, err := filepath.Glob(filepath.Join(templatesRoot, "partials", "*.gohtml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(partials)

	pages := make([]PageTemplate, 0, len(files))
	for _, pageFile := range files {
		name := strings.TrimSuffix(filepath.Base(pageFile), filepath.Ext(pageFile))
		parseFiles := []string{filepath.Join(templatesRoot, "layout", "base.gohtml")}
		parseFiles = append(parseFiles, partials...)
		parseFiles = append(parseFiles, pageFile)

		tpl, err := template.New("layout").Funcs(template.FuncMap{
			"dict":       dict,
			"list":       list,
			"safeHTML":   safeHTML,
			"toJSON":     toJSON,
			"dig":        dig,
			"required":   required,
			"trimSuffix": strings.TrimSuffix,
			"trimPrefix": strings.TrimPrefix,
			"has":        hasString,
			"pageSlug":   pageSlugFunc,
		}).ParseFiles(parseFiles...)
		if err != nil {
			return nil, err
		}
		pages = append(pages, PageTemplate{Name: name, Tmpl: tpl})
	}

	sort.Slice(pages, func(i, j int) bool {
		return pages[i].Name < pages[j].Name
	})

	return pages, nil
}

func LoadSiteData(templatesRoot, overrideSource string) (SiteDataLoadResult, error) {
	defaultPath := filepath.Join(filepath.Dir(templatesRoot), "data", "site.yaml")
	defaultFound, err := fileExists(defaultPath)
	if err != nil {
		return SiteDataLoadResult{}, fmt.Errorf("stat default site data: %w", err)
	}

	source := strings.TrimSpace(overrideSource)
	if source != "" {
		return loadSiteDataFromOverride(source, defaultPath, defaultFound)
	}

	overlayDir := filepath.Join(filepath.Dir(templatesRoot), "data", "site.d")
	overlayLayers, err := listYAMLLayersInDirIfExists(overlayDir)
	if err != nil {
		return SiteDataLoadResult{}, err
	}

	if !defaultFound && len(overlayLayers) == 0 {
		return SiteDataLoadResult{
			Data:             map[string]any{},
			DefaultPath:      defaultPath,
			DefaultPathFound: false,
		}, nil
	}

	base, baseOrigin, layers, err := loadBaseSiteData(defaultFound, defaultPath, overlayLayers)
	if err != nil {
		return SiteDataLoadResult{}, err
	}

	merged, err := mergeSiteDataStrict(base, overlayLayers, baseOrigin)
	if err != nil {
		return SiteDataLoadResult{}, err
	}

	return buildSiteDataLoadResult(defaultFound, defaultPath, layers, merged), nil
}

func loadSiteDataFromOverride(source, defaultPath string, defaultFound bool) (SiteDataLoadResult, error) {
	data, layers, err := loadLayersFromSource(source)
	if err != nil {
		return SiteDataLoadResult{}, err
	}
	return SiteDataLoadResult{
		Data:             data,
		Source:           source,
		Layers:           layers,
		DefaultPath:      defaultPath,
		UsedOverride:     true,
		DefaultPathFound: defaultFound,
	}, nil
}

func loadLayersFromSource(source string) (map[string]any, []string, error) {
	if parts := strings.Split(source, "|"); len(parts) > 1 {
		return loadMultiDirSource(parts)
	}
	if isLocalDirSource(source) {
		layers, err := listYAMLLayersInDir(source)
		if err != nil {
			return nil, nil, err
		}
		merged, err := loadAndMergeSiteDataLayers(layers)
		return merged, layers, err
	}
	raw, err := readDataSource(source)
	if err != nil {
		return nil, nil, err
	}
	siteData, err := parseSiteData(raw)
	return siteData, []string{source}, err
}

func loadMultiDirSource(parts []string) (map[string]any, []string, error) {
	var allLayers []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		layers, err := listYAMLLayersInDir(part)
		if err != nil {
			return nil, nil, err
		}
		allLayers = append(allLayers, layers...)
	}
	merged, err := loadAndMergeSiteDataLayers(allLayers)
	return merged, allLayers, err
}

func loadBaseSiteData(defaultFound bool, defaultPath string, overlayLayers []string) (base map[string]any, baseOrigin string, layers []string, err error) {
	if !defaultFound {
		return map[string]any{}, "", overlayLayers, nil
	}

	raw, err := readDataSource(defaultPath)
	if err != nil {
		return nil, "", nil, err
	}
	siteData, err := parseSiteData(raw)
	if err != nil {
		return nil, "", nil, err
	}

	layers = append(layers, defaultPath)
	layers = append(layers, overlayLayers...)
	return siteData, defaultPath, layers, nil
}

func buildSiteDataLoadResult(defaultFound bool, defaultPath string, layers []string, merged map[string]any) SiteDataLoadResult {
	resultSource := ""
	if defaultFound {
		resultSource = defaultPath
	}
	return SiteDataLoadResult{
		Data:             merged,
		Source:           resultSource,
		Layers:           layers,
		DefaultPath:      defaultPath,
		UsedOverride:     false,
		DefaultPathFound: defaultFound,
	}
}

func LoadSiteDataContract(templatesRoot string) (SiteDataContractLoadResult, error) {
	defaultPath := filepath.Join(filepath.Dir(templatesRoot), "data", "site.contract.yaml")
	defaultFound, err := fileExists(defaultPath)
	if err != nil {
		return SiteDataContractLoadResult{}, fmt.Errorf("stat default site data contract: %w", err)
	}
	if !defaultFound {
		return SiteDataContractLoadResult{}, fmt.Errorf(
			"required site data contract not found: %s",
			defaultPath,
		)
	}

	raw, err := readDataSource(defaultPath)
	if err != nil {
		return SiteDataContractLoadResult{}, err
	}

	contract, err := parseSiteDataContract(raw)
	if err != nil {
		return SiteDataContractLoadResult{}, err
	}

	return SiteDataContractLoadResult{
		Contract:    contract,
		Source:      defaultPath,
		DefaultPath: defaultPath,
	}, nil
}

// SiteInputs bundles the three loaded artifacts that every command needs.
type SiteInputs struct {
	Pages                  []PageTemplate
	SiteDataResult         SiteDataLoadResult
	SiteDataContractResult SiteDataContractLoadResult
}

// LoadSiteInputs loads templates, site data, and the site data contract from
// templatesDir. It does not perform any validation; callers decide what to
// validate based on their requirements.
func LoadSiteInputs(templatesDir, siteDataSource string) (SiteInputs, error) {
	pages, err := LoadPageTemplatesFromRoot(templatesDir)
	if err != nil {
		return SiteInputs{}, fmt.Errorf("loading templates: %w", err)
	}
	siteDataResult, err := LoadSiteData(templatesDir, siteDataSource)
	if err != nil {
		return SiteInputs{}, fmt.Errorf("loading site data: %w", err)
	}
	contractResult, err := LoadSiteDataContract(templatesDir)
	if err != nil {
		return SiteInputs{}, fmt.Errorf("loading site data contract: %w", err)
	}
	return SiteInputs{
		Pages:                  pages,
		SiteDataResult:         siteDataResult,
		SiteDataContractResult: contractResult,
	}, nil
}

// RenderPages executes every page template and returns a map of page name →
// rendered HTML string.
func RenderPages(pages []PageTemplate, siteData map[string]any) (map[string]string, error) {
	renderedPages := make(map[string]string, len(pages))
	for _, page := range pages {
		var rendered bytes.Buffer
		if err := page.Tmpl.ExecuteTemplate(&rendered, "layout", NewTemplateData(page.Name, siteData)); err != nil {
			return nil, fmt.Errorf("rendering %s.html: %w", page.Name, err)
		}
		renderedPages[page.Name] = rendered.String()
	}
	return renderedPages, nil
}

// ValidateSiteDataAndUsage validates site data against its contract, then
// checks that every contract path is referenced by at least one template.
func ValidateSiteDataAndUsage(pages []PageTemplate, siteDataResult SiteDataLoadResult, siteDataContractResult SiteDataContractLoadResult) error {
	if err := ValidateSiteData(siteDataResult.Data, siteDataContractResult.Contract); err != nil {
		return fmt.Errorf("validating site data against contract: %w", err)
	}

	contract := siteDataContractResult.Contract
	if len(contract.Required) == 0 && len(contract.Allowed) == 0 {
		return nil
	}

	usedPaths, err := TraceSiteDataUsage(pages, siteDataResult.Data)
	if err != nil {
		return fmt.Errorf("tracing site data usage: %w", err)
	}
	if err := ValidateSiteDataContractUsage(contract, usedPaths); err != nil {
		return fmt.Errorf("validating site data contract usage: %w", err)
	}
	return nil
}

// ValidateSiteDataAndUsageFromRoot is like ValidateSiteDataAndUsage but loads
// page templates lazily from templatesRoot (only when the contract is non-empty).
func ValidateSiteDataAndUsageFromRoot(templatesRoot string, siteDataResult SiteDataLoadResult, siteDataContractResult SiteDataContractLoadResult) error {
	if err := ValidateSiteData(siteDataResult.Data, siteDataContractResult.Contract); err != nil {
		return fmt.Errorf("validating site data against contract: %w", err)
	}

	contract := siteDataContractResult.Contract
	if len(contract.Required) == 0 && len(contract.Allowed) == 0 {
		return nil
	}

	pages, err := LoadPageTemplatesFromRoot(templatesRoot)
	if err != nil {
		return fmt.Errorf("loading templates for site data usage validation: %w", err)
	}
	usedPaths, err := TraceSiteDataUsage(pages, siteDataResult.Data)
	if err != nil {
		return fmt.Errorf("tracing site data usage: %w", err)
	}
	if err := ValidateSiteDataContractUsage(contract, usedPaths); err != nil {
		return fmt.Errorf("validating site data contract usage: %w", err)
	}
	return nil
}

func isHTTPURL(v string) bool {
	return strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://")
}

func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
