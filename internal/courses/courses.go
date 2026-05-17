package courses

import (
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// Course holds data for a single course catalog entry.
type Course struct {
	Title               string            `yaml:"title"`
	Description         string            `yaml:"description"`
	Platform            string            `yaml:"platform"`
	Level               string            `yaml:"level"`
	AvailabilityType    string            `yaml:"availability_type"`
	UdemyURL            string            `yaml:"udemy_url"`
	SupportedLanguages  []string          `yaml:"supported_languages"`
	LocalizedCTALabels  map[string]string `yaml:"localized_cta_labels"`
	LocalizedPriceNotes string            `yaml:"localized_price_notes"`
	Order               int               `yaml:"order"`
}

// LoadCoursesFile reads a YAML list of courses from path and returns them
// sorted ascending by Order, then by Title for ties.
func LoadCoursesFile(path string) ([]Course, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading courses file %s: %w", path, err)
	}

	var list []Course
	if err := yaml.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("parsing courses file %s: %w", path, err)
	}

	for i, c := range list {
		if c.Title == "" {
			return nil, fmt.Errorf("courses[%d]: missing required field 'title'", i)
		}
	}

	sort.Slice(list, func(i, j int) bool {
		if list[i].Order != list[j].Order {
			return list[i].Order < list[j].Order
		}
		return list[i].Title < list[j].Title
	})

	return list, nil
}

// ToSiteDataList converts a slice of Course to the []any map format expected
// by Go templates via the dig/range template functions.
func ToSiteDataList(list []Course) []any {
	out := make([]any, len(list))
	for i, c := range list {
		langs := make([]any, len(c.SupportedLanguages))
		for j, l := range c.SupportedLanguages {
			langs[j] = l
		}
		ctaLabels := make(map[string]any, len(c.LocalizedCTALabels))
		for k, v := range c.LocalizedCTALabels {
			ctaLabels[k] = v
		}
		out[i] = map[string]any{
			"title":                 c.Title,
			"description":           c.Description,
			"platform":              c.Platform,
			"level":                 c.Level,
			"availability_type":     c.AvailabilityType,
			"udemy_url":             c.UdemyURL,
			"supported_languages":   langs,
			"localized_cta_labels":  ctaLabels,
			"localized_price_notes": c.LocalizedPriceNotes,
			"order":                 c.Order,
		}
	}
	return out
}
