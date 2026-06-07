package buildcmd

import (
	"log/slog"

	"ffreis-website-compiler/internal/courses"
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
	return writePaginatedPages(paginatedPagesParams{
		logger:      logger,
		opts:        opts,
		tmpl:        projectTpl,
		items:       projects.ToSiteDataList(list),
		sectionName: "projects",
		siteData:    siteData,
		assetsDir:   assetsDir,
		mirrorer:    mirrorer,
	})
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
	return writePaginatedPages(paginatedPagesParams{
		logger:      logger,
		opts:        opts,
		tmpl:        coursesTpl,
		items:       courses.ToSiteDataList(list),
		sectionName: "courses",
		siteData:    siteData,
		assetsDir:   assetsDir,
		mirrorer:    mirrorer,
	})
}
