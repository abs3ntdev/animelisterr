package feed

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// RankingPost identifies a weekly ranking article on animecorner.me.
type RankingPost struct {
	Title     string
	URL       string
	Published time.Time
	Season    string // e.g. "spring"
	Year      int
	Week      int
	Slug      string // e.g. "spring-2026-anime-rankings-week-2"
}

// weekly ranking titles look like "Spring 2026 Anime Rankings - Week 2".
var titleRE = regexp.MustCompile(`(?i)^(spring|summer|fall|autumn|winter)\s+(\d{4})\s+anime\s+rankings\s*[-–—]\s*week\s+(\d+)`)

// slugRE extracts season/year/week from the canonical URL slug as a fallback
// when the RSS title uses a different separator character.
var slugRE = regexp.MustCompile(`(?i)(spring|summer|fall|autumn|winter)-(\d{4})-anime-rankings-week-(\d+)`)

type rss struct {
	Channel struct {
		Items []struct {
			Title   string `xml:"title"`
			Link    string `xml:"link"`
			PubDate string `xml:"pubDate"`
		} `xml:"item"`
	} `xml:"channel"`
}

// Client fetches the Anime Corner weekly-rankings feed and returns the most
// recent ranking post.
type Client struct {
	FeedURL   string
	UserAgent string
	HTTP      *http.Client
}

// New returns a Client with sensible defaults.
func New(feedURL, userAgent string) *Client {
	return &Client{
		FeedURL:   feedURL,
		UserAgent: userAgent,
		HTTP:      &http.Client{Timeout: 30 * time.Second},
	}
}

// Latest returns the most recently published "Anime Rankings - Week N" post
// from the RSS feed. Non-ranking items (news, reviews, etc.) are skipped.
func (c *Client) Latest(ctx context.Context) (*RankingPost, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.FeedURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Accept", "application/rss+xml, application/xml, text/xml")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch feed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("feed returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read feed: %w", err)
	}

	var parsed rss
	if err := xml.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse feed: %w", err)
	}

	// RSS items are newest-first. Return the first one matching the ranking
	// title pattern.
	for _, it := range parsed.Channel.Items {
		post := classify(it.Title, it.Link)
		if post == nil {
			continue
		}
		if t, err := parsePubDate(it.PubDate); err == nil {
			post.Published = t
		}
		return post, nil
	}
	return nil, fmt.Errorf("no ranking post found in feed")
}

// classify inspects an RSS item's title (and URL as fallback) and returns a
// RankingPost with parsed season/year/week, or nil if the item isn't a
// weekly ranking.
func classify(title, link string) *RankingPost {
	t := strings.TrimSpace(title)
	if m := titleRE.FindStringSubmatch(t); m != nil {
		year, _ := strconv.Atoi(m[2])
		week, _ := strconv.Atoi(m[3])
		return &RankingPost{
			Title:  t,
			URL:    link,
			Season: strings.ToLower(m[1]),
			Year:   year,
			Week:   week,
			Slug:   slugFromURL(link),
		}
	}
	// fall back to URL slug matching for malformed titles
	if m := slugRE.FindStringSubmatch(link); m != nil {
		year, _ := strconv.Atoi(m[2])
		week, _ := strconv.Atoi(m[3])
		return &RankingPost{
			Title:  t,
			URL:    link,
			Season: strings.ToLower(m[1]),
			Year:   year,
			Week:   week,
			Slug:   slugFromURL(link),
		}
	}
	return nil
}

func slugFromURL(u string) string {
	u = strings.TrimRight(u, "/")
	if i := strings.LastIndex(u, "/"); i >= 0 {
		return u[i+1:]
	}
	return u
}

// parsePubDate handles both RFC1123Z and RFC1123 timestamps that the feed
// uses in practice.
func parsePubDate(s string) (time.Time, error) {
	layouts := []string{time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised date %q", s)
}
