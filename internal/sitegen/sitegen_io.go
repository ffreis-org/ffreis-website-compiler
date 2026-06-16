package sitegen

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

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
