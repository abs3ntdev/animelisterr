package scrape

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// RankedEntry is one row of the weekly Top-N table.
type RankedEntry struct {
	Rank  int
	Title string
	Votes string // raw e.g. "10.15%"
}

// Scraper fetches a ranking post and parses its ranking table.
type Scraper struct {
	UserAgent string
	HTTP      *http.Client
}

// New returns a Scraper with a default 30s HTTP timeout.
func New(userAgent string) *Scraper {
	return &Scraper{
		UserAgent: userAgent,
		HTTP:      &http.Client{Timeout: 30 * time.Second},
	}
}

var rankRE = regexp.MustCompile(`^\s*(\d+)\s*(?:st|nd|rd|th)?\s*$`)

// Top fetches the article at url and returns the first `limit` ranked
// entries from the article's ranking table. Rows that fail to parse are
// skipped rather than aborting the whole scrape.
func (s *Scraper) Top(ctx context.Context, url string, limit int) ([]RankedEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", s.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := s.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("post returned status %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}

	// Primary selector: the Gutenberg ranking table inside the post body.
	table := doc.Find("div.entry-content figure.wp-block-table table").First()
	if table.Length() == 0 {
		// Fallback: any table inside entry-content.
		table = doc.Find("div.entry-content table").First()
	}
	if table.Length() == 0 {
		return nil, fmt.Errorf("no ranking table found on page")
	}

	rows := table.Find("tbody tr")
	if rows.Length() == 0 {
		// Some WP themes omit tbody; match all rows whose first cell is td.
		rows = table.Find("tr").FilterFunction(func(_ int, r *goquery.Selection) bool {
			return r.Find("td").Length() > 0
		})
	}

	out := make([]RankedEntry, 0, limit)
	rows.EachWithBreak(func(_ int, r *goquery.Selection) bool {
		if len(out) >= limit {
			return false
		}
		cells := r.Find("td")
		if cells.Length() < 2 {
			return true
		}
		rankText := strings.TrimSpace(cells.Eq(0).Text())
		titleText := strings.TrimSpace(cells.Eq(1).Text())
		votesText := ""
		if cells.Length() >= 3 {
			votesText = strings.TrimSpace(cells.Eq(2).Text())
		}
		m := rankRE.FindStringSubmatch(rankText)
		if m == nil || titleText == "" {
			return true
		}
		rank, err := strconv.Atoi(m[1])
		if err != nil {
			return true
		}
		out = append(out, RankedEntry{
			Rank:  rank,
			Title: titleText,
			Votes: votesText,
		})
		return true
	})

	if len(out) == 0 {
		return nil, fmt.Errorf("table found but no rows parsed")
	}
	return out, nil
}
