package courses

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"MLOps in Production: From Notebook to Platform": "mlops-in-production-from-notebook-to-platform",
		"AWS for ML Engineers!":                          "aws-for-ml-engineers",
		"  Trailing & leading  ":                         "trailing-leading",
		"Already-a-slug":                                 "already-a-slug",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func writeCoursesFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "courses.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing temp courses file: %v", err)
	}
	return path
}

func TestLoadCoursesFile_DerivesSlugWhenAbsent(t *testing.T) {
	path := writeCoursesFile(t, `
- title: "MLOps in Production"
  order: 1
- title: "Explicit"
  order: 2
  slug: "custom-slug"
`)
	list, err := LoadCoursesFile(path)
	if err != nil {
		t.Fatalf("LoadCoursesFile: %v", err)
	}
	if list[0].Slug != "mlops-in-production" {
		t.Errorf("derived slug = %q, want %q", list[0].Slug, "mlops-in-production")
	}
	if list[1].Slug != "custom-slug" {
		t.Errorf("explicit slug should be preserved, got %q", list[1].Slug)
	}
}

func TestToSiteDataList_IncludesLandingFields(t *testing.T) {
	list := []Course{{
		Title:         "RAG Systems",
		Slug:          "rag-systems",
		SaleMode:      "both",
		PriceUsdCents: 3900,
		PriceBrlCents: 19900,
		UdemyURL:      "https://udemy.com/x",
	}}
	got := ToSiteDataList(list)
	m := got[0].(map[string]any)
	if m["href"] != "/courses/rag-systems/" {
		t.Errorf("href = %v, want /courses/rag-systems/", m["href"])
	}
	if m["sale_mode"] != "both" {
		t.Errorf("sale_mode = %v, want both", m["sale_mode"])
	}
	if m["price_usd_cents"] != 3900 {
		t.Errorf("price_usd_cents = %v, want 3900", m["price_usd_cents"])
	}
}

func TestToCurrentCourse_HasLongFormContent(t *testing.T) {
	c := Course{
		Title:           "MLOps",
		Slug:            "mlops",
		LongDescription: "a longer description",
		WhatYouLearn:    []string{"a", "b"},
		Curriculum: []Module{
			{Title: "Module 1", Lessons: []string{"L1", "L2"}},
		},
	}
	m := ToCurrentCourse(c)

	if m["long_description"] != "a longer description" {
		t.Errorf("long_description missing: %v", m["long_description"])
	}
	learn, ok := m["what_you_learn"].([]any)
	if !ok || len(learn) != 2 {
		t.Fatalf("what_you_learn should be a 2-element list, got %v", m["what_you_learn"])
	}
	curriculum, ok := m["curriculum"].([]any)
	if !ok || len(curriculum) != 1 {
		t.Fatalf("curriculum should be a 1-element list, got %v", m["curriculum"])
	}
	mod := curriculum[0].(map[string]any)
	if mod["title"] != "Module 1" {
		t.Errorf("module title = %v, want Module 1", mod["title"])
	}
	lessons, ok := mod["lessons"].([]any)
	if !ok || len(lessons) != 2 {
		t.Fatalf("module lessons should be a 2-element list, got %v", mod["lessons"])
	}
}
