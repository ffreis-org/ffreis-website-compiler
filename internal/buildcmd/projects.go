package buildcmd

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	"ffreis-website-compiler/internal/courses"
	"ffreis-website-compiler/internal/pagination"
	"ffreis-website-compiler/internal/projects"
	"ffreis-website-compiler/internal/sitegen"
	"ffreis-website-compiler/internal/sitemap"
)

// injectProjectsHomeCarousel puts the first n projects into siteData["projects"]
// so the home-page carousel template can range over them.
func injectProjectsHomeCarousel(siteData map[string]any, list []projects.Project, n int) {
	all := projects.ToSiteDataList(list)
	if n > 0 && n < len(all) {
		all = all[:n]
	}
	siteData["projects"] = all
}

// injectCoursesHomeCarousel puts the first n courses into
// siteData["pages"]["index"]["courses_carousel_items"] for the home carousel.
func injectCoursesHomeCarousel(siteData map[string]any, list []courses.Course, n int) {
	all := courses.ToSiteDataList(list)
	if n > 0 && n < len(all) {
		all = all[:n]
	}
	pagesData, _ := siteData["pages"].(map[string]any)
	if pagesData == nil {
		return
	}
	indexData, _ := pagesData["index"].(map[string]any)
	if indexData == nil {
		indexData = make(map[string]any)
		pagesData["index"] = indexData
	}
	indexData["courses_carousel_items"] = all
}

// writeProjectPages generates /projects/index.html (page 1) and
// /projects/page/N/index.html for every subsequent page.
func writeProjectPages(
	logger *slog.Logger,
	opts buildOptions,
	projectTpl sitegen.PageTemplate,
	list []projects.Project,
	siteData map[string]any,
	assetsDir string,
	mirrorer *externalAssetMirrorer,
) ([]sitemap.URLItem, error) {
	allItems := projects.ToSiteDataList(list)
	pages := pagination.Paginate(allItems, opts.itemsPerPage, "/projects")
	allToCopy := make(map[string]string)
	var sitemapURLs []sitemap.URLItem

	for _, pg := range pages {
		pageData := shallowCopySiteDataForSection(siteData, "projects")
		injectPaginatedSection(pageData, "projects", pg, opts.itemsPerPage)

		var rendered bytes.Buffer
		if err := projectTpl.Tmpl.ExecuteTemplate(&rendered, "layout",
			sitegen.NewTemplateData("projects", pageData)); err != nil {
			return nil, fmt.Errorf("rendering projects page %d: %w", pg.Number, err)
		}

		htmlOut, toCopy, err := transformPage(rendered.String(), opts, assetsDir, mirrorer)
		if err != nil {
			return nil, fmt.Errorf("transforming projects page %d: %w", pg.Number, err)
		}
		for k, v := range toCopy {
			allToCopy[k] = v
		}

		target := resolvePagedTarget(opts.outDir, "projects", pg.Number)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, fmt.Errorf("creating dir for projects page %d: %w", pg.Number, err)
		}
		if err := os.WriteFile(target, []byte(htmlOut), 0o644); err != nil { //nolint:gosec
			return nil, fmt.Errorf(errFmtWriting, target, err)
		}
		logger.Info("generated projects page", "page", pg.Number, "target", target)

		sitemapURLs = append(sitemapURLs, sitemap.URLItem{
			Path: pagination.PageHref("/projects", pg.Number),
		})
	}

	if err := writeHashedAssets(opts.outDir, assetsDir, allToCopy); err != nil {
		return nil, err
	}
	return sitemapURLs, nil
}

// writeCoursePages generates /courses/index.html (page 1) and
// /courses/page/N/index.html for every subsequent page.
func writeCoursePages(
	logger *slog.Logger,
	opts buildOptions,
	coursesTpl sitegen.PageTemplate,
	list []courses.Course,
	siteData map[string]any,
	assetsDir string,
	mirrorer *externalAssetMirrorer,
) ([]sitemap.URLItem, error) {
	allItems := courses.ToSiteDataList(list)
	pages := pagination.Paginate(allItems, opts.itemsPerPage, "/courses")
	allToCopy := make(map[string]string)
	var sitemapURLs []sitemap.URLItem

	for _, pg := range pages {
		pageData := shallowCopySiteDataForSection(siteData, "courses")
		injectPaginatedSection(pageData, "courses", pg, opts.itemsPerPage)

		var rendered bytes.Buffer
		if err := coursesTpl.Tmpl.ExecuteTemplate(&rendered, "layout",
			sitegen.NewTemplateData("courses", pageData)); err != nil {
			return nil, fmt.Errorf("rendering courses page %d: %w", pg.Number, err)
		}

		htmlOut, toCopy, err := transformPage(rendered.String(), opts, assetsDir, mirrorer)
		if err != nil {
			return nil, fmt.Errorf("transforming courses page %d: %w", pg.Number, err)
		}
		for k, v := range toCopy {
			allToCopy[k] = v
		}

		target := resolvePagedTarget(opts.outDir, "courses", pg.Number)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, fmt.Errorf("creating dir for courses page %d: %w", pg.Number, err)
		}
		if err := os.WriteFile(target, []byte(htmlOut), 0o644); err != nil { //nolint:gosec
			return nil, fmt.Errorf(errFmtWriting, target, err)
		}
		logger.Info("generated courses page", "page", pg.Number, "target", target)

		sitemapURLs = append(sitemapURLs, sitemap.URLItem{
			Path: pagination.PageHref("/courses", pg.Number),
		})
	}

	if err := writeHashedAssets(opts.outDir, assetsDir, allToCopy); err != nil {
		return nil, err
	}
	return sitemapURLs, nil
}

// shallowCopySiteDataForSection returns a copy of siteData where the named
// section under pages is replaced by a fresh map, preventing cross-page
// mutations from one paginated render bleeding into the next.
func shallowCopySiteDataForSection(siteData map[string]any, section string) map[string]any {
	// Shallow-copy top level
	copy := make(map[string]any, len(siteData))
	for k, v := range siteData {
		copy[k] = v
	}

	// Shallow-copy pages map
	origPages, _ := siteData["pages"].(map[string]any)
	pagesCopy := make(map[string]any, len(origPages))
	for k, v := range origPages {
		pagesCopy[k] = v
	}
	copy["pages"] = pagesCopy

	// Shallow-copy the target section map
	origSection, _ := origPages[section].(map[string]any)
	sectionCopy := make(map[string]any, len(origSection))
	for k, v := range origSection {
		sectionCopy[k] = v
	}
	pagesCopy[section] = sectionCopy

	return copy
}

// injectPaginatedSection writes the paginated items and pagination metadata
// into siteData["pages"][section] for a single page render.
func injectPaginatedSection(siteData map[string]any, section string, pg pagination.Page, perPage int) {
	pagesData, _ := siteData["pages"].(map[string]any)
	if pagesData == nil {
		return
	}
	sectionData, _ := pagesData[section].(map[string]any)
	if sectionData == nil {
		sectionData = make(map[string]any)
		pagesData[section] = sectionData
	}
	sectionData["items"] = pg.Items
	sectionData["pagination"] = pagination.ToSiteDataMap(pg, perPage)
}

// resolvePagedTarget returns the output file path for a paginated page.
// Page 1 → <outDir>/<section>/index.html
// Page N → <outDir>/<section>/page/<N>/index.html
func resolvePagedTarget(outDir, section string, pageNum int) string {
	if pageNum == 1 {
		return filepath.Join(outDir, section, "index.html")
	}
	return filepath.Join(outDir, section, "page", strconv.Itoa(pageNum), "index.html")
}
