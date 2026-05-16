package rss

import (
	"encoding/xml"
	"fmt"
	"time"

	"ffreis-website-compiler/internal/posts"
)

// FeedConfig holds site-level configuration for the RSS feed.
type FeedConfig struct {
	Title       string
	Link        string // e.g. https://ffreis.com
	Description string
	Language    string // e.g. "en"
}

type rssRoot struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title         string    `xml:"title"`
	Link          string    `xml:"link"`
	Description   string    `xml:"description"`
	Language      string    `xml:"language"`
	LastBuildDate string    `xml:"lastBuildDate"`
	Items         []rssItem `xml:"item"`
}

type rssItem struct {
	Title       string  `xml:"title"`
	Link        string  `xml:"link"`
	Description string  `xml:"description"`
	GUID        rssGUID `xml:"guid"`
	PubDate     string  `xml:"pubDate"`
}

type rssGUID struct {
	IsPermaLink string `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}

// GenerateRSS returns RSS 2.0 XML bytes for the given posts.
// Each item uses CanonicalURL as the link and GUID.
func GenerateRSS(cfg FeedConfig, postList []posts.Post) ([]byte, error) {
	items := make([]rssItem, 0, len(postList))
	for _, p := range postList {
		pubDate, err := parsePostDate(p.Meta.Date)
		if err != nil {
			return nil, fmt.Errorf("parsing date for post %s: %w", p.Meta.Slug, err)
		}

		canonicalURL := p.Meta.CanonicalURL
		if canonicalURL == "" {
			canonicalURL = cfg.Link + "/blog/" + p.Meta.Slug + "/"
		}

		items = append(items, rssItem{
			Title:       p.Meta.Title,
			Link:        canonicalURL,
			Description: p.Meta.Summary,
			GUID:        rssGUID{IsPermaLink: "true", Value: canonicalURL},
			PubDate:     pubDate.Format(time.RFC1123Z),
		})
	}

	feed := rssRoot{
		Version: "2.0",
		Channel: rssChannel{
			Title:         cfg.Title,
			Link:          cfg.Link,
			Description:   cfg.Description,
			Language:      cfg.Language,
			LastBuildDate: time.Now().UTC().Format(time.RFC1123Z),
			Items:         items,
		},
	}

	output, err := xml.MarshalIndent(feed, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshalling RSS feed: %w", err)
	}
	return append([]byte(xml.Header), output...), nil
}

func parsePostDate(dateStr string) (time.Time, error) {
	formats := []string{"2006-01-02", "2006-01-02T15:04:05Z07:00"}
	for _, f := range formats {
		if t, err := time.Parse(f, dateStr); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised date format %q (expected YYYY-MM-DD)", dateStr)
}
