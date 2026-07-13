package courses

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Module is one section of a course curriculum.
type Module struct {
	Title   string   `yaml:"title"`
	Lessons []string `yaml:"lessons"`
}

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

	// Landing-page + checkout fields.
	Slug            string   `yaml:"slug"`
	SaleMode        string   `yaml:"sale_mode"`
	PriceUsdCents   int      `yaml:"price_usd_cents"`
	PriceBrlCents   int      `yaml:"price_brl_cents"`
	LongDescription string   `yaml:"long_description"`
	WhatYouLearn    []string `yaml:"what_you_learn"`
	Curriculum      []Module `yaml:"curriculum"`
}

var (
	slugNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)
	slugTrim     = regexp.MustCompile(`^-+|-+$`)
)

// Slugify converts a title to a URL slug: lowercase, non-alphanumerics collapsed
// to single hyphens, trimmed. "MLOps in Production!" -> "mlops-in-production".
func Slugify(title string) string {
	s := slugNonAlnum.ReplaceAllString(strings.ToLower(title), "-")
	return slugTrim.ReplaceAllString(s, "")
}

// coursePath is the site path for a course's landing page.
func coursePath(slug string) string {
	return "/courses/" + slug + "/"
}

// formatMoney renders a price in cents as a currency string, dropping ".00" for
// whole amounts. 4900 -> "$49"; 4999 -> "$49.99"; 0 -> "" (no price).
func formatMoney(symbol string, cents int) string {
	if cents <= 0 {
		return ""
	}
	whole := cents / 100
	frac := cents % 100
	if frac == 0 {
		return fmt.Sprintf("%s%d", symbol, whole)
	}
	return fmt.Sprintf("%s%d.%02d", symbol, whole, frac)
}

// LoadCoursesFile reads a YAML list of courses from path and returns them
// sorted ascending by Order, then by Title for ties. A missing slug is derived
// from the title so every course has a stable landing-page path.
func LoadCoursesFile(path string) ([]Course, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading courses file %s: %w", path, err)
	}

	var list []Course
	if err := yaml.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("parsing courses file %s: %w", path, err)
	}

	for i := range list {
		if list[i].Title == "" {
			return nil, fmt.Errorf("courses[%d]: missing required field 'title'", i)
		}
		if list[i].Slug == "" {
			list[i].Slug = Slugify(list[i].Title)
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

// baseMap returns the fields shared by the catalog list and the landing page.
func baseMap(c Course) map[string]any {
	langs := make([]any, len(c.SupportedLanguages))
	for j, l := range c.SupportedLanguages {
		langs[j] = l
	}
	ctaLabels := make(map[string]any, len(c.LocalizedCTALabels))
	for k, v := range c.LocalizedCTALabels {
		ctaLabels[k] = v
	}
	return map[string]any{
		"title":                 c.Title,
		"slug":                  c.Slug,
		"href":                  coursePath(c.Slug),
		"description":           c.Description,
		"platform":              c.Platform,
		"level":                 c.Level,
		"availability_type":     c.AvailabilityType,
		"sale_mode":             c.SaleMode,
		"price_usd_cents":       c.PriceUsdCents,
		"price_brl_cents":       c.PriceBrlCents,
		"price_usd_display":     formatMoney("$", c.PriceUsdCents),
		"price_brl_display":     formatMoney("R$", c.PriceBrlCents),
		"udemy_url":             c.UdemyURL,
		"supported_languages":   langs,
		"localized_cta_labels":  ctaLabels,
		"localized_price_notes": c.LocalizedPriceNotes,
		"order":                 c.Order,
	}
}

// ToSiteDataList converts a slice of Course to the []any map format expected
// by Go templates via the dig/range template functions (the /courses/ listing
// and the home carousel).
func ToSiteDataList(list []Course) []any {
	out := make([]any, len(list))
	for i, c := range list {
		out[i] = baseMap(c)
	}
	return out
}

// ToCurrentCourse builds the per-course landing-page data map (the "CurrentCourse"
// template key), extending the catalog fields with the long-form landing content.
func ToCurrentCourse(c Course) map[string]any {
	m := baseMap(c)

	learn := make([]any, len(c.WhatYouLearn))
	for i, item := range c.WhatYouLearn {
		learn[i] = item
	}
	m["what_you_learn"] = learn

	modules := make([]any, len(c.Curriculum))
	for i, mod := range c.Curriculum {
		lessons := make([]any, len(mod.Lessons))
		for j, l := range mod.Lessons {
			lessons[j] = l
		}
		modules[i] = map[string]any{"title": mod.Title, "lessons": lessons}
	}
	m["curriculum"] = modules
	m["long_description"] = c.LongDescription
	return m
}
