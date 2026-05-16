package buildcmd

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	"ffreis-website-compiler/internal/pagination"
	"ffreis-website-compiler/internal/sitegen"
	"ffreis-website-compiler/internal/sitemap"
)

// writePaginatedPages generates page 1 at /<sectionName>/index.html and
// subsequent pages at /<sectionName>/page/N/index.html.
//
// items must already be converted to []any by the caller (e.g. via
// projects.ToSiteDataList, courses.ToSiteDataList, or postToMap).
//
// It returns the sitemap URL entries for all generated pages.
func writePaginatedPages(
	logger *slog.Logger,
	opts buildOptions,
	tmpl sitegen.PageTemplate,
	items []any,
	sectionName string,
	siteData map[string]any,
	assetsDir string,
	mirrorer *externalAssetMirrorer,
) ([]sitemap.URLItem, error) {
	basePath := "/" + sectionName
	pages := pagination.Paginate(items, opts.itemsPerPage, basePath)
	allToCopy := make(map[string]string)
	var sitemapURLs []sitemap.URLItem

	for _, pg := range pages {
		pageData := shallowCopySiteDataForSection(siteData, sectionName)
		injectPaginatedSection(pageData, sectionName, pg, opts.itemsPerPage)

		var rendered bytes.Buffer
		if err := tmpl.Tmpl.ExecuteTemplate(&rendered, "layout",
			sitegen.NewTemplateData(sectionName, pageData)); err != nil {
			return nil, fmt.Errorf("rendering %s page %d: %w", sectionName, pg.Number, err)
		}

		htmlOut, toCopy, err := transformPage(rendered.String(), opts, assetsDir, mirrorer)
		if err != nil {
			return nil, fmt.Errorf("transforming %s page %d: %w", sectionName, pg.Number, err)
		}
		for k, v := range toCopy {
			allToCopy[k] = v
		}

		target := resolvePagedTarget(opts.outDir, sectionName, pg.Number)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, fmt.Errorf("creating dir for %s page %d: %w", sectionName, pg.Number, err)
		}
		if err := os.WriteFile(target, []byte(htmlOut), 0o644); err != nil { //nolint:gosec
			return nil, fmt.Errorf(errFmtWriting, target, err)
		}
		logger.Info("generated paginated page", "section", sectionName, "page", pg.Number, "target", target)

		sitemapURLs = append(sitemapURLs, sitemap.URLItem{
			Path: pagination.PageHref(basePath, pg.Number),
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
	out := make(map[string]any, len(siteData))
	for k, v := range siteData {
		out[k] = v
	}

	origPages, _ := siteData["pages"].(map[string]any)
	pagesCopy := make(map[string]any, len(origPages))
	for k, v := range origPages {
		pagesCopy[k] = v
	}
	out["pages"] = pagesCopy

	origSection, _ := origPages[section].(map[string]any)
	sectionCopy := make(map[string]any, len(origSection))
	for k, v := range origSection {
		sectionCopy[k] = v
	}
	pagesCopy[section] = sectionCopy

	return out
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
// Page 1 → <outDir>/<section>/index.html.
// Page N → <outDir>/<section>/page/<N>/index.html.
func resolvePagedTarget(outDir, section string, pageNum int) string {
	if pageNum == 1 {
		return filepath.Join(outDir, section, "index.html")
	}
	return filepath.Join(outDir, section, "page", strconv.Itoa(pageNum), "index.html")
}
