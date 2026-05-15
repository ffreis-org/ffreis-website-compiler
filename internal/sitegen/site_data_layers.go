package sitegen

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func isLocalDirSource(source string) bool {
	if strings.TrimSpace(source) == "" {
		return false
	}
	if isHTTPURL(source) || strings.HasPrefix(source, "file://") {
		return false
	}
	parsed, err := os.Stat(source)
	return err == nil && parsed.IsDir()
}

func listYAMLLayersInDirIfExists(dir string) ([]string, error) {
	info, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%q is not a directory", dir)
	}

	layers, err := listYAMLLayersInDir(dir)
	if err != nil {
		return nil, err
	}
	// For implicit overlays, allow empty directories.
	return layers, nil
}

func listYAMLLayersInDir(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		files = append(files, filepath.Join(dir, name))
	}
	sort.Strings(files)

	// Also include files from a site.d/ subdirectory, sorted after top-level files.
	subDir := filepath.Join(dir, "site.d")
	subLayers, err := listYAMLLayersInDirIfExists(subDir)
	if err != nil {
		return nil, err
	}
	files = append(files, subLayers...)
	return files, nil
}

func loadAndMergeSiteDataLayers(layers []string) (map[string]any, error) {
	if len(layers) == 0 {
		return nil, fmt.Errorf("no YAML layers provided")
	}

	raw, err := readDataSource(layers[0])
	if err != nil {
		return nil, err
	}
	base, err := parseSiteData(raw)
	if err != nil {
		return nil, err
	}

	merged, err := mergeSiteDataStrict(base, layers[1:], layers[0])
	if err != nil {
		return nil, err
	}
	return merged, nil
}

func mergeSiteDataStrict(base map[string]any, layerFiles []string, baseOrigin string) (map[string]any, error) {
	origins := make(map[string]string, 1024)
	indexOriginsForMap(base, baseOrigin, "", origins)

	for _, layer := range layerFiles {
		if baseOrigin != "" && layer == baseOrigin {
			continue
		}
		raw, err := readDataSource(layer)
		if err != nil {
			return nil, err
		}
		next, err := parseSiteData(raw)
		if err != nil {
			return nil, err
		}
		if err := mergeIntoStrict(base, next, "", layer, origins); err != nil {
			return nil, err
		}
	}

	return base, nil
}

func mergeIntoStrict(dst, src map[string]any, prefix, layer string, origins map[string]string) error {
	for key, srcVal := range src {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}

		dstVal, exists := dst[key]
		if !exists {
			dst[key] = srcVal
			indexOriginsForValue(srcVal, layer, path, origins)
			continue
		}

		dstMap, dstIsMap := dstVal.(map[string]any)
		srcMap, srcIsMap := srcVal.(map[string]any)
		if dstIsMap && srcIsMap {
			if err := mergeIntoStrict(dstMap, srcMap, path, layer, origins); err != nil {
				return err
			}
			continue
		}

		first := origins[path]
		if strings.TrimSpace(first) == "" {
			first = "<unknown>"
		}
		return fmt.Errorf("conflicting site data path %q first defined in %s and again in %s", path, first, layer)
	}
	return nil
}

func indexOriginsForMap(m map[string]any, origin, prefix string, origins map[string]string) {
	if m == nil || strings.TrimSpace(origin) == "" {
		return
	}
	for key, value := range m {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		indexOriginsForValue(value, origin, path, origins)
	}
}

func indexOriginsForValue(value any, origin, path string, origins map[string]string) {
	if strings.TrimSpace(origin) == "" || strings.TrimSpace(path) == "" {
		return
	}
	if _, ok := origins[path]; !ok {
		origins[path] = origin
	}

	childMap, ok := value.(map[string]any)
	if !ok {
		return
	}
	for key, item := range childMap {
		nextPath := path + "." + key
		indexOriginsForValue(item, origin, nextPath, origins)
	}
}
