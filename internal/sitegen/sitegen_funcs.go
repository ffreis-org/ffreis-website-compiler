package sitegen

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"reflect"
	"strings"
)

// pageSlugFunc returns the URL slug for pageName by reading pages.<pageName>.slug
// from siteData, falling back to pageName when the field is absent.
func pageSlugFunc(siteData any, pageName string) string {
	sd, _ := siteData.(map[string]any)
	pagesData, _ := sd["pages"].(map[string]any)
	pageData, _ := pagesData[pageName].(map[string]any)
	slug, _ := pageData["slug"].(string)
	if slug == "" {
		return pageName
	}
	return slug
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
		m[k] = values[i+1] //nolint:gosec // template dict helper; keys and values come from trusted site YAML, not user input
	}
	return m, nil
}

func list(values ...any) []any {
	return values
}

func safeHTML(v string) template.HTML {
	return template.HTML(v) //nolint:gosec // false positive: input is pre-rendered HTML from trusted site YAML, not user content
}

// hasString reports whether val is present in slice. The slice argument accepts
// []any (as returned by template data) and []string. Any other type returns false.
// Used by listing templates to check content item language availability.
func hasString(slice any, val string) bool {
	switch s := slice.(type) {
	case []any:
		for _, item := range s {
			if str, ok := item.(string); ok && str == val {
				return true
			}
		}
	case []string:
		for _, str := range s {
			if str == val {
				return true
			}
		}
	}
	return false
}

// toJSON marshals v to JSON and marks it safe for embedding in a <script> block.
func toJSON(v any) (template.JS, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("toJSON: %w", err)
	}
	return template.JS(b), nil //nolint:gosec // json.Marshal output is safe for <script> embedding; it auto-escapes <, >, & as < etc.
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
