package sitegen

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func ValidateSiteData(siteData map[string]any, contract SiteDataContract) error {
	requiredPatterns, err := normalizePatterns(contract.Required)
	if err != nil {
		return err
	}
	allowedPatterns, err := normalizePatterns(contract.Allowed)
	if err != nil {
		return err
	}
	compilerConsumedPatterns, err := normalizePatterns(contract.CompilerConsumed)
	if err != nil {
		return err
	}

	if len(requiredPatterns) == 0 && len(allowedPatterns) == 0 && len(compilerConsumedPatterns) == 0 {
		return nil
	}

	allPaths, leafPaths := collectPaths(siteData)
	var validationErrors []string
	validationErrors = append(validationErrors, missingRequiredPathErrors(allPaths, requiredPatterns)...)
	validationErrors = append(validationErrors, danglingPathErrors(leafPaths, allowedPatterns, compilerConsumedPatterns)...)

	if len(validationErrors) > 0 {
		return errors.New(strings.Join(validationErrors, "; "))
	}
	return nil
}

// missingRequiredPathErrors returns an error string for each required pattern
// that has no matching path in allPaths.
func missingRequiredPathErrors(allPaths, requiredPatterns []string) []string {
	var errs []string
	for _, pattern := range requiredPatterns {
		if !anyPathMatches(allPaths, pattern) {
			errs = append(errs, fmt.Sprintf("missing required site data path: %s", pattern))
		}
	}
	return errs
}

// danglingPathErrors returns an error string for each leaf path that is not
// matched by any allowed or compiler-consumed pattern.
func danglingPathErrors(leafPaths, allowedPatterns, compilerConsumedPatterns []string) []string {
	if len(allowedPatterns) == 0 && len(compilerConsumedPatterns) == 0 {
		return nil
	}
	var errs []string
	for _, path := range leafPaths {
		if !anyPatternMatches(path, allowedPatterns) && !anyPatternMatches(path, compilerConsumedPatterns) {
			errs = append(errs, fmt.Sprintf("dangling site data path not declared in contract: %s", path))
		}
	}
	return errs
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
	compilerConsumedPatterns, err := normalizePatterns(contract.CompilerConsumed)
	if err != nil {
		return err
	}

	if len(requiredPatterns) == 0 && len(allowedPatterns) == 0 {
		return nil
	}

	validationErrors := make([]string, 0)
	// compiler_consumed paths are allowed for template access but never required.
	allKnownPatterns := append(allowedPatterns, compilerConsumedPatterns...)
	validationErrors = append(validationErrors, undeclaredUsageErrors(usedPaths, allKnownPatterns, requiredPatterns)...)
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
