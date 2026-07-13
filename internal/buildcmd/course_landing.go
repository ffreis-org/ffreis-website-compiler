package buildcmd

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"ffreis-website-compiler/internal/courses"
	"ffreis-website-compiler/internal/sitegen"
	"ffreis-website-compiler/internal/sitemap"
)

// maybeWriteCourseLandingPages renders one landing page per course at
// /courses/<slug>/ using the internal "course" template, when that template and
// loaded courses are both available. Returns the landing-page URLs for the sitemap.
func maybeWriteCourseLandingPages(
	logger *slog.Logger,
	opts buildOptions,
	content *optionalContent,
	siteData map[string]any,
	assetsDir string,
	mirrorer *externalAssetMirrorer,
) ([]sitemap.URLItem, error) {
	if content.courseTemplate == nil || len(content.courses) == 0 {
		return nil, nil
	}
	return writeCourseLandingPages(logger, opts, *content.courseTemplate, content.courses, siteData, assetsDir, mirrorer)
}

// writeCourseLandingPages renders and writes one HTML file per course using the
// course template, and returns a sitemap URL item for each.
func writeCourseLandingPages(
	logger *slog.Logger,
	opts buildOptions,
	courseTpl sitegen.PageTemplate,
	list []courses.Course,
	siteData map[string]any,
	assetsDir string,
	mirrorer *externalAssetMirrorer,
) ([]sitemap.URLItem, error) {
	allToCopy := make(map[string]string)
	urls := make([]sitemap.URLItem, 0, len(list))

	for _, c := range list {
		toCopy, err := renderAndWriteCourse(logger, opts, courseTpl, c, siteData, assetsDir, mirrorer)
		if err != nil {
			return nil, err
		}
		for k, v := range toCopy {
			allToCopy[k] = v
		}
		urls = append(urls, sitemap.URLItem{
			Path:       "/courses/" + c.Slug + "/",
			Changefreq: "monthly",
			Priority:   "0.6",
		})
	}

	if err := writeHashedAssets(opts.outDir, assetsDir, allToCopy); err != nil {
		return nil, err
	}
	return urls, nil
}

// renderAndWriteCourse renders a single course landing page and writes it to
// /courses/<slug>/index.html. Returns the asset-copy map for deferred fingerprinting.
func renderAndWriteCourse(
	logger *slog.Logger,
	opts buildOptions,
	courseTpl sitegen.PageTemplate,
	c courses.Course,
	siteData map[string]any,
	assetsDir string,
	mirrorer *externalAssetMirrorer,
) (map[string]string, error) {
	templateData := map[string]any{
		"PageName":      "course",
		"SiteData":      siteData,
		"CurrentCourse": courses.ToCurrentCourse(c),
	}

	var rendered bytes.Buffer
	if err := courseTpl.Tmpl.ExecuteTemplate(&rendered, "layout", templateData); err != nil {
		return nil, fmt.Errorf("rendering course %s: %w", c.Slug, err)
	}

	htmlOut, toCopy, err := transformPage(rendered.String(), opts, assetsDir, mirrorer)
	if err != nil {
		return nil, fmt.Errorf("transforming course %s: %w", c.Slug, err)
	}

	target := filepath.Join(opts.outDir, "courses", c.Slug, "index.html")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, fmt.Errorf("creating directory for course %s: %w", c.Slug, err)
	}
	if err := os.WriteFile(target, []byte(htmlOut), 0o644); err != nil { //nolint:gosec
		return nil, fmt.Errorf(errFmtWriting, target, err)
	}
	logger.Info("generated course landing page", "slug", c.Slug, "target", target)
	return toCopy, nil
}
