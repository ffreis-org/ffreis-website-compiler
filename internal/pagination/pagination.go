// Package pagination provides build-time page chunking and pagination bar
// computation for the static site compiler.
package pagination

import "strconv"

// BarItem represents one element in a rendered pagination bar.
type BarItem struct {
	Type   string // "page" or "ellipsis"
	Number int    // page number (0 for ellipsis)
	Href   string // URL for the page link
	Active bool   // true when this is the current page
	IsLast bool   // true when this is the last page (gets "(last)" label)
}

// Page holds the items and pagination metadata for a single output page.
type Page struct {
	Number     int
	TotalPages int
	Items      []any
	Bar        []BarItem
	BaseHref   string
}

// PageHref returns the URL for a given page number under a base section path.
// Page 1 maps to baseHref + "/" and page N maps to baseHref + "/page/N/".
func PageHref(baseHref string, pageNum int) string {
	if pageNum == 1 {
		return baseHref + "/"
	}
	return baseHref + "/page/" + strconv.Itoa(pageNum) + "/"
}

// Paginate splits items into pages of perPage items each and computes the
// pagination bar for each page.
func Paginate(items []any, perPage int, baseHref string) []Page {
	if perPage <= 0 {
		perPage = 12
	}
	total := len(items)
	if total == 0 {
		return []Page{{Number: 1, TotalPages: 1, Items: nil, Bar: nil}}
	}

	totalPages := (total + perPage - 1) / perPage
	pages := make([]Page, totalPages)

	for i := range pages {
		pageNum := i + 1
		start := i * perPage
		end := start + perPage
		if end > total {
			end = total
		}
		pages[i] = Page{
			Number:     pageNum,
			TotalPages: totalPages,
			Items:      items[start:end],
			Bar:        computeBar(pageNum, totalPages, baseHref),
			BaseHref:   baseHref,
		}
	}

	return pages
}

// ToSiteDataMap converts a Page into the map[string]any shape injected into
// SiteData so Go templates can access it via the dig template function.
func ToSiteDataMap(pg Page, perPage int) map[string]any {
	bar := make([]any, len(pg.Bar))
	for i, item := range pg.Bar {
		bar[i] = map[string]any{
			"type":   item.Type,
			"number": item.Number,
			"href":   item.Href,
			"active": item.Active,
		}
	}
	prevHref := ""
	nextHref := ""
	if pg.Number > 1 {
		prevHref = PageHref(pg.BaseHref, pg.Number-1)
	}
	if pg.Number < pg.TotalPages {
		nextHref = PageHref(pg.BaseHref, pg.Number+1)
	}
	return map[string]any{
		"current_page":   pg.Number,
		"total_pages":    pg.TotalPages,
		"items_per_page": perPage,
		"bar":            bar,
		"prev_href":      prevHref,
		"next_href":      nextHref,
	}
}

// computeBar builds the pagination bar for page P of T total pages.
//
// Pattern: [1] [ellipsis if P>2] [P if P≠1 and P≠T] [ellipsis if P<T-1] [T(last)]
//
// Examples (P=5, T=7):  1 … 5 … 7(last)
//          (P=2, T=7):  1 2 … 7(last)
//          (P=6, T=7):  1 … 6 7(last)
//          (P=1, T=1):  1(last)
func computeBar(currentPage, totalPages int, baseHref string) []BarItem {
	if totalPages <= 1 {
		return []BarItem{{
			Type:   "page",
			Number: 1,
			Href:   PageHref(baseHref, 1),
			Active: true,
			IsLast: true,
		}}
	}

	var bar []BarItem

	// First page
	bar = append(bar, BarItem{
		Type:   "page",
		Number: 1,
		Href:   PageHref(baseHref, 1),
		Active: currentPage == 1,
		IsLast: totalPages == 1,
	})

	// Left ellipsis: shown when current is more than one step away from first
	if currentPage > 2 {
		bar = append(bar, BarItem{Type: "ellipsis"})
	}

	// Current page (only if it's not the first or last)
	if currentPage != 1 && currentPage != totalPages {
		bar = append(bar, BarItem{
			Type:   "page",
			Number: currentPage,
			Href:   PageHref(baseHref, currentPage),
			Active: true,
		})
	}

	// Right ellipsis: shown when current is more than one step away from last
	if currentPage < totalPages-1 {
		bar = append(bar, BarItem{Type: "ellipsis"})
	}

	// Last page
	bar = append(bar, BarItem{
		Type:   "page",
		Number: totalPages,
		Href:   PageHref(baseHref, totalPages),
		Active: currentPage == totalPages,
		IsLast: true,
	})

	return bar
}
