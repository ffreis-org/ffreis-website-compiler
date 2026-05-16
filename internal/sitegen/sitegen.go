package sitegen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
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
}

type SiteDataContractLoadResult struct {
	Contract    SiteDataContract
	Source      string
	DefaultPath string
}

type tracedMap map[string]any
type tracedSlice []any

type accessTracer struct {
	mu         sync.Mutex
	used       map[string]struct{}
	registered []uintptr
}

type traceMetadata struct {
	path   string
	tracer *accessTracer
}

var traceRegistry sync.Map

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

func ValidateSiteData(siteData map[string]any, contract SiteDataContract) error {
	requiredPatterns, err := normalizePatterns(contract.Required)
	if err != nil {
		return err
	}
	allowedPatterns, err := normalizePatterns(contract.Allowed)
	if err != nil {
		return err
	}

	if len(requiredPatterns) == 0 && len(allowedPatterns) == 0 {
		return nil
	}

	allPaths, leafPaths := collectPaths(siteData)
	var validationErrors []string

	for _, pattern := range requiredPatterns {
		if !anyPathMatches(allPaths, pattern) {
			validationErrors = append(validationErrors, fmt.Sprintf("missing required site data path: %s", pattern))
		}
	}

	if len(allowedPatterns) > 0 {
		for _, path := range leafPaths {
			if !anyPatternMatches(path, allowedPatterns) {
				validationErrors = append(validationErrors, fmt.Sprintf("dangling site data path not declared in contract: %s", path))
			}
		}
	}

	if len(validationErrors) > 0 {
		return errors.New(strings.Join(validationErrors, "; "))
	}
	return nil
}

func ValidateSiteDataContractUsage(contract SiteDataContract, usedPaths []string) error {
	requiredPatterns, err := normalizePatterns(contract.Required)
	if err != nil {
		return err
	}
	allowedPatterns, err := normalizePatterns(contract.Allowed)
	if err != nil {
		return err
	}

	if len(requiredPatterns) == 0 && len(allowedPatterns) == 0 {
		return nil
	}

	validationErrors := make([]string, 0)
	validationErrors = append(validationErrors, undeclaredUsageErrors(usedPaths, allowedPatterns, requiredPatterns)...)
	validationErrors = append(validationErrors, unusedPatternErrors("required", requiredPatterns, usedPaths)...)
	validationErrors = append(validationErrors, unusedPatternErrors("allowed", allowedPatterns, usedPaths)...)

	if len(validationErrors) == 0 {
		return nil
	}
	return errors.New(strings.Join(validationErrors, "; "))
}

func undeclaredUsageErrors(usedPaths, allowedPatterns, requiredPatterns []string) []string {
	var errs []string
	for _, path := range usedPaths {
		if anyPatternMatches(path, allowedPatterns) || anyPatternMatches(path, requiredPatterns) {
			continue
		}
		errs = append(errs, fmt.Sprintf("site data path used by templates but not declared in contract: %s", path))
	}
	return errs
}

func unusedPatternErrors(kind string, patterns []string, usedPaths []string) []string {
	var errs []string
	for _, pattern := range patterns {
		if anyPathMatches(usedPaths, pattern) {
			continue
		}
		errs = append(errs, fmt.Sprintf("%s contract path not used by templates: %s", kind, pattern))
	}
	return errs
}

func TraceSiteDataUsage(pages []PageTemplate, siteData map[string]any) ([]string, error) {
	tracer := &accessTracer{
		used: make(map[string]struct{}),
	}
	defer tracer.cleanup()
	tracedData := wrapTracedValue(siteData, tracer, "")

	for _, page := range pages {
		if err := page.Tmpl.ExecuteTemplate(io.Discard, "layout", NewTemplateData(page.Name, tracedData)); err != nil {
			return nil, fmt.Errorf("rendering %s for site data trace: %w", page.Name, err)
		}
	}
	return tracer.usedPaths(), nil
}

func NewTemplateData(pageName string, siteData any) map[string]any {
	if siteData == nil {
		siteData = map[string]any{}
	}
	return map[string]any{
		"PageName": pageName,
		"SiteData": siteData,
	}
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

func readDataSource(source string) ([]byte, error) {
	if source == "" {
		return nil, fmt.Errorf("data source cannot be empty")
	}
	if isHTTPURL(source) {
		return readDataURL(source)
	}
	if strings.HasPrefix(source, "file://") {
		fileURL, err := url.Parse(source)
		if err != nil {
			return nil, fmt.Errorf("parsing data file URL: %w", err)
		}
		return readDataFile(fileURL.Path)
	}
	if parsed, err := url.Parse(source); err == nil && parsed.Scheme != "" && parsed.Scheme != "file" {
		return nil, fmt.Errorf("unsupported data source scheme %q", parsed.Scheme)
	}
	return readDataFile(source)
}

func readDataFile(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading data source: %w", err)
	}
	return raw, nil
}

func readDataURL(rawURL string) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request for data source: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching data source: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetching data source: unexpected status %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading data source response: %w", err)
	}
	return body, nil
}

func parseSiteData(raw []byte) (map[string]any, error) {
	var siteData map[string]any
	if err := yaml.Unmarshal(raw, &siteData); err != nil {
		return nil, fmt.Errorf("parsing site data yaml: %w", err)
	}
	if siteData == nil {
		return map[string]any{}, nil
	}

	normalized, err := normalizeYAMLValue(siteData)
	if err != nil {
		return nil, fmt.Errorf("normalizing site data: %w", err)
	}

	root, ok := normalized.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("site data root must be a map")
	}
	return root, nil
}

func parseSiteDataContract(raw []byte) (SiteDataContract, error) {
	var contract SiteDataContract
	if err := yaml.Unmarshal(raw, &contract); err != nil {
		return SiteDataContract{}, fmt.Errorf("parsing site data contract yaml: %w", err)
	}
	requiredPatterns, err := normalizePatterns(contract.Required)
	if err != nil {
		return SiteDataContract{}, err
	}
	allowedPatterns, err := normalizePatterns(contract.Allowed)
	if err != nil {
		return SiteDataContract{}, err
	}
	contract.Required = requiredPatterns
	contract.Allowed = allowedPatterns
	return contract, nil
}

func dict(values ...any) (map[string]any, error) {
	if len(values)%2 != 0 {
		return nil, fmt.Errorf("dict expects an even number of arguments")
	}
	m := make(map[string]any, len(values)/2)
	for i := 0; i < len(values); i += 2 {
		k, ok := values[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict keys must be strings")
		}
		m[k] = values[i+1] //nolint:gosec
	}
	return m, nil
}

func list(values ...any) []any {
	return values
}

func safeHTML(v string) template.HTML {
	return template.HTML(v) //nolint:gosec
}

// toJSON marshals v to JSON and marks it safe for embedding in a <script> block.
func toJSON(v any) (template.JS, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("toJSON: %w", err)
	}
	return template.JS(b), nil //nolint:gosec
}

func dig(root any, keys ...any) (any, error) {
	current := root
	tracePath, tracer := traceStateForValue(current)

	for _, key := range keys {
		if current == nil {
			return nil, nil
		}

		next, nextTracePath, err := descend(current, key, tracePath, tracer)
		if err != nil {
			return nil, err
		}
		if next == nil && current != nil {
			// For missing keys/out-of-range indexes, preserve original behavior (nil, nil).
			return nil, nil
		}

		current = next
		tracePath = nextTracePath
		tracePath, tracer = updateTraceStateFromMetadata(current, tracePath, tracer)
	}

	recordTraceIfNeeded(tracer, tracePath, current)
	return current, nil
}

func traceStateForValue(value any) (string, *accessTracer) {
	if metadata, ok := traceMetadataForValue(value); ok {
		return metadata.path, metadata.tracer
	}
	return "", nil
}

func updateTraceStateFromMetadata(value any, tracePath string, tracer *accessTracer) (string, *accessTracer) {
	if nextMetadata, ok := traceMetadataForValue(value); ok {
		return nextMetadata.path, nextMetadata.tracer
	}
	return tracePath, tracer
}

func recordTraceIfNeeded(tracer *accessTracer, tracePath string, value any) {
	if tracer == nil {
		return
	}
	if tracePath == "" {
		return
	}
	if !shouldTraceValue(value) {
		return
	}
	tracer.record(tracePath)
}

func descend(current any, key any, tracePath string, tracer *accessTracer) (any, string, error) {
	switch typed := current.(type) {
	case map[string]any:
		return descendMapLike(typed, key, tracePath, tracer)
	case tracedMap:
		return descendMapLike(map[string]any(typed), key, tracePath, tracer)
	case []any:
		return descendSliceLike(typed, key, tracePath, tracer)
	case tracedSlice:
		return descendSliceLike([]any(typed), key, tracePath, tracer)
	default:
		return nil, tracePath, fmt.Errorf("dig cannot descend into %T", current)
	}
}

func descendMapLike(m map[string]any, key any, tracePath string, tracer *accessTracer) (any, string, error) {
	keyString, err := stringifyKey(key)
	if err != nil {
		return nil, tracePath, err
	}
	next := m[keyString]
	if tracer != nil {
		tracePath = joinTracePath(tracePath, keyString)
	}
	return next, tracePath, nil
}

func descendSliceLike(s []any, key any, tracePath string, tracer *accessTracer) (any, string, error) {
	index, err := integerKey(key)
	if err != nil {
		return nil, tracePath, err
	}
	if index < 0 || index >= len(s) {
		return nil, tracePath, nil
	}
	next := s[index]
	if tracer != nil {
		tracePath = joinTracePath(tracePath, strconv.Itoa(index))
	}
	return next, tracePath, nil
}

func required(value any, message string) (any, error) {
	if isMissingValue(value) {
		return nil, errors.New(message)
	}
	return value, nil
}

func normalizeYAMLValue(value any) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		normalized := make(map[string]any, len(typed))
		for key, item := range typed {
			next, err := normalizeYAMLValue(item)
			if err != nil {
				return nil, err
			}
			normalized[key] = next
		}
		return normalized, nil
	case []any:
		normalized := make([]any, len(typed))
		for i, item := range typed {
			next, err := normalizeYAMLValue(item)
			if err != nil {
				return nil, err
			}
			normalized[i] = next
		}
		return normalized, nil
	default:
		return value, nil
	}
}

func stringifyKey(key any) (string, error) {
	switch typed := key.(type) {
	case string:
		return typed, nil
	case fmt.Stringer:
		return typed.String(), nil
	default:
		return "", fmt.Errorf("dig map keys must be strings, got %T", key)
	}
}

func integerKey(key any) (int, error) {
	switch typed := key.(type) {
	case int:
		return typed, nil
	case int8:
		return int(typed), nil
	case int16:
		return int(typed), nil
	case int32:
		return int(typed), nil
	case int64:
		return int(typed), nil
	case uint:
		return int(typed), nil //nolint:gosec
	case uint8:
		return int(typed), nil
	case uint16:
		return int(typed), nil
	case uint32:
		return int(typed), nil
	case uint64:
		return int(typed), nil //nolint:gosec
	case string:
		index, err := strconv.Atoi(typed)
		if err != nil {
			return 0, fmt.Errorf("dig slice keys must be integers, got %q", typed)
		}
		return index, nil
	default:
		return 0, fmt.Errorf("dig slice keys must be integers, got %T", key)
	}
}

func isMissingValue(value any) bool {
	if value == nil {
		return true
	}

	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) == ""
	}

	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface:
		return rv.IsNil()
	case reflect.Slice, reflect.Map, reflect.Array:
		return rv.Len() == 0
	}

	return false
}

func isHTTPURL(v string) bool {
	return strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://")
}

func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func normalizePatterns(patterns []string) ([]string, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	normalized := make([]string, 0, len(patterns))
	seen := make(map[string]struct{}, len(patterns))
	for _, raw := range patterns {
		pattern := strings.Trim(strings.TrimSpace(raw), ".")
		if pattern == "" {
			continue
		}
		segments := strings.Split(pattern, ".")
		for _, segment := range segments {
			if strings.TrimSpace(segment) == "" {
				return nil, fmt.Errorf("invalid site data contract pattern %q", raw)
			}
		}
		if _, ok := seen[pattern]; ok {
			continue
		}
		seen[pattern] = struct{}{}
		normalized = append(normalized, pattern)
	}
	sort.Strings(normalized)
	return normalized, nil
}

func collectPaths(root map[string]any) ([]string, []string) {
	var allPaths []string
	var leafPaths []string
	walkPaths(nil, root, &allPaths, &leafPaths)
	return allPaths, leafPaths
}

func walkPaths(prefix []string, value any, allPaths *[]string, leafPaths *[]string) {
	if len(prefix) > 0 {
		*allPaths = append(*allPaths, strings.Join(prefix, "."))
	}

	switch typed := value.(type) {
	case map[string]any:
		walkPathsMap(prefix, typed, allPaths, leafPaths)
	case []any:
		walkPathsSlice(prefix, typed, allPaths, leafPaths)
	default:
		if len(prefix) > 0 {
			*leafPaths = append(*leafPaths, strings.Join(prefix, "."))
		}
	}
}

func walkPathsMap(prefix []string, typed map[string]any, allPaths *[]string, leafPaths *[]string) {
	if len(typed) == 0 && len(prefix) > 0 {
		*leafPaths = append(*leafPaths, strings.Join(prefix, "."))
		return
	}

	keys := make([]string, 0, len(typed))
	for key := range typed {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		walkPaths(append(prefix, key), typed[key], allPaths, leafPaths)
	}
}

func walkPathsSlice(prefix []string, typed []any, allPaths *[]string, leafPaths *[]string) {
	if len(typed) == 0 && len(prefix) > 0 {
		*leafPaths = append(*leafPaths, strings.Join(prefix, "."))
		return
	}
	for i, item := range typed {
		walkPaths(append(prefix, strconv.Itoa(i)), item, allPaths, leafPaths)
	}
}

func anyPathMatches(paths []string, pattern string) bool {
	for _, path := range paths {
		if pathMatchesPattern(path, pattern) {
			return true
		}
	}
	return false
}

func anyPatternMatches(path string, patterns []string) bool {
	for _, pattern := range patterns {
		if pathMatchesPattern(path, pattern) {
			return true
		}
	}
	return false
}

func wrapTracedValue(value any, tracer *accessTracer, path string) any {
	switch typed := value.(type) {
	case map[string]any:
		wrapped := make(tracedMap, len(typed))
		for key, item := range typed {
			wrapped[key] = wrapTracedValue(item, tracer, joinTracePath(path, key))
		}
		registerTraceMetadata(wrapped, traceMetadata{path: path, tracer: tracer})
		return wrapped
	case []any:
		wrapped := make(tracedSlice, len(typed))
		for i, item := range typed {
			wrapped[i] = wrapTracedValue(item, tracer, joinTracePath(path, strconv.Itoa(i)))
		}
		registerTraceMetadata(wrapped, traceMetadata{path: path, tracer: tracer})
		return wrapped
	default:
		return value
	}
}

func registerTraceMetadata(value any, metadata traceMetadata) {
	pointer, ok := compositePointer(value)
	if !ok || pointer == 0 {
		return
	}
	traceRegistry.Store(pointer, metadata)
	if metadata.tracer != nil {
		metadata.tracer.mu.Lock()
		metadata.tracer.registered = append(metadata.tracer.registered, pointer)
		metadata.tracer.mu.Unlock()
	}
}

func traceMetadataForValue(value any) (traceMetadata, bool) {
	pointer, ok := compositePointer(value)
	if !ok || pointer == 0 {
		return traceMetadata{}, false
	}
	metadata, ok := traceRegistry.Load(pointer)
	if !ok {
		return traceMetadata{}, false
	}
	typed, ok := metadata.(traceMetadata)
	return typed, ok
}

func compositePointer(value any) (uintptr, bool) {
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Map, reflect.Slice:
		return rv.Pointer(), true
	default:
		return 0, false
	}
}

func shouldTraceValue(value any) bool {
	switch value.(type) {
	case tracedMap, map[string]any:
		return false
	default:
		return true
	}
}

func joinTracePath(prefix, segment string) string {
	if strings.TrimSpace(prefix) == "" {
		return segment
	}
	return prefix + "." + segment
}

func (t *accessTracer) record(path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.used[path] = struct{}{}
}

func (t *accessTracer) usedPaths() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	paths := make([]string, 0, len(t.used))
	for path := range t.used {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func (t *accessTracer) cleanup() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, pointer := range t.registered {
		traceRegistry.Delete(pointer)
	}
	t.registered = nil
}

func pathMatchesPattern(path, pattern string) bool {
	pathSegments := strings.Split(strings.Trim(path, "."), ".")
	patternSegments := strings.Split(strings.Trim(pattern, "."), ".")
	if len(patternSegments) > len(pathSegments) {
		return false
	}
	for i, segment := range patternSegments {
		if segment == "*" {
			continue
		}
		if segment != pathSegments[i] {
			return false
		}
	}
	return true
}
