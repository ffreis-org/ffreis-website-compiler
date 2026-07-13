package buildcmd

import (
	"testing"

	"ffreis-website-compiler/internal/sitegen"
	"ffreis-website-compiler/internal/sitemap"
)

func TestSectionEnabled_DefaultsToEnabledWhenAbsent(t *testing.T) {
	if !sectionEnabled(map[string]any{}, "blog") {
		t.Fatal("section with no sections map should default to enabled")
	}
	sd := map[string]any{"sections": map[string]any{"courses": true}}
	if !sectionEnabled(sd, "blog") {
		t.Fatal("section absent from sections map should default to enabled")
	}
}

func TestSectionEnabled_RespectsFalse(t *testing.T) {
	sd := map[string]any{"sections": map[string]any{"blog": false, "courses": true}}
	if sectionEnabled(sd, "blog") {
		t.Fatal("blog explicitly false must be disabled")
	}
	if !sectionEnabled(sd, "courses") {
		t.Fatal("courses explicitly true must be enabled")
	}
}

func TestFilterInternalPages_DropsDisabledSectionPages(t *testing.T) {
	pages := []sitegen.PageTemplate{
		{Name: "index"}, {Name: "about"},
		{Name: "blog"}, {Name: "post"},
		{Name: "courses"}, {Name: "projects"},
	}
	sd := map[string]any{"sections": map[string]any{"blog": false, "courses": false, "projects": true}}

	got := filterInternalPages(pages, sd)
	names := map[string]bool{}
	for _, p := range got {
		names[p.Name] = true
	}
	for _, dropped := range []string{"blog", "post", "courses"} {
		if names[dropped] {
			t.Errorf("disabled-section page %q should have been dropped", dropped)
		}
	}
	for _, kept := range []string{"index", "about", "projects"} {
		if !names[kept] {
			t.Errorf("page %q should have been kept", kept)
		}
	}
}

func TestFilterDisabledSectionURLs_DropsMatchingPaths(t *testing.T) {
	urls := []sitemap.URLItem{
		{Path: "/"}, {Path: "/about/"},
		{Path: "/blog/"}, {Path: "/courses/"}, {Path: "/projects/"},
	}
	sd := map[string]any{"sections": map[string]any{"blog": false, "courses": false, "projects": true}}

	got := filterDisabledSectionURLs(urls, sd)
	kept := map[string]bool{}
	for _, u := range got {
		kept[u.Path] = true
	}
	if kept["/blog/"] || kept["/courses/"] {
		t.Error("disabled-section sitemap paths should be dropped")
	}
	for _, want := range []string{"/", "/about/", "/projects/"} {
		if !kept[want] {
			t.Errorf("path %q should have been kept", want)
		}
	}
}

func TestSectionForPath(t *testing.T) {
	cases := map[string]string{
		"/blog/": "blog", "/blog/a-post/": "blog",
		"/courses/": "courses", "/projects/": "projects",
		"/": "", "/about/": "", "/blogging/": "",
	}
	for path, want := range cases {
		if got := sectionForPath(path); got != want {
			t.Errorf("sectionForPath(%q) = %q, want %q", path, got, want)
		}
	}
}
