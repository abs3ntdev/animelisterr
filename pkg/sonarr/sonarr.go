package sonarr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

// Season mirrors the `seasons[]` entries in Sonarr's SeriesResource.
type Season struct {
	SeasonNumber int  `json:"seasonNumber"`
	Monitored    bool `json:"monitored"`
}

// AlternateTitle mirrors one element of SeriesResource.alternateTitles.
type AlternateTitle struct {
	Title string `json:"title"`
}

// Statistics mirrors SeriesResource.statistics. `SeasonCount` is already the
// count of regular (non-special) seasons — it's what Sonarr's own UI shows.
type Statistics struct {
	SeasonCount int `json:"seasonCount"`
}

// Language mirrors the `originalLanguage` object. Its `name` ("Japanese",
// "English", etc.) is the cleanest anime-vs-not signal we can pull from
// Sonarr's lookup: Anime Corner's rankings are always Japanese originals.
type Language struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// Ratings mirrors SeriesResource.ratings. `Votes` is used as a confidence
// signal — a result with thousands of votes is almost always a real show,
// while a zero-vote result is often an obscure false match.
type Ratings struct {
	Votes int     `json:"votes"`
	Value float64 `json:"value"`
}

// Series is the subset of SeriesResource we use. Field names and types
// match Sonarr v3's published OpenAPI schema exactly; unused fields (path,
// images, tags, addOptions, etc.) are intentionally omitted.
type Series struct {
	Title            string           `json:"title"`
	SortTitle        string           `json:"sortTitle"`
	Year             int              `json:"year"`
	Status           string           `json:"status"`
	Ended            bool             `json:"ended"`
	Overview         string           `json:"overview"`
	TvdbID           int              `json:"tvdbId"`
	TmdbID           int              `json:"tmdbId"`
	TvMazeID         int              `json:"tvMazeId"`
	TvRageID         int              `json:"tvRageId"`
	ImdbID           string           `json:"imdbId"`
	TitleSlug        string           `json:"titleSlug"`
	Runtime          int              `json:"runtime"`
	Network          string           `json:"network"`
	SeriesType       string           `json:"seriesType"`
	Genres           []string         `json:"genres"`
	Seasons          []Season         `json:"seasons"`
	AlternateTitles  []AlternateTitle `json:"alternateTitles"`
	Statistics       Statistics       `json:"statistics"`
	OriginalLanguage Language         `json:"originalLanguage"`
	Ratings          Ratings          `json:"ratings"`
	FirstAired       string           `json:"firstAired"`
	LastAired        string           `json:"lastAired"`
}

// IsJapaneseOriginal reports whether the series' original language is
// Japanese. This is the most reliable "is it actually anime?" signal
// exposed by Sonarr: it comes from TVDB metadata and is independent of
// whether any user ever tagged the series as anime.
func (s *Series) IsJapaneseOriginal() bool {
	return strings.EqualFold(s.OriginalLanguage.Name, "Japanese")
}

// HasAnimeGenre reports whether "Anime" appears in the series' genres. A
// weaker signal than IsJapaneseOriginal — some TVDB entries list both
// "Animation" and "Anime", some only "Animation".
func (s *Series) HasAnimeGenre() bool {
	for _, g := range s.Genres {
		if strings.EqualFold(g, "Anime") {
			return true
		}
	}
	return false
}

// RegularSeasonCount returns the count of non-special seasons. It prefers
// Sonarr's pre-computed `statistics.seasonCount` (which already excludes
// season 0) and falls back to walking `seasons[]` for the rare result that
// omits statistics.
func (s *Series) RegularSeasonCount() int {
	if s.Statistics.SeasonCount > 0 {
		return s.Statistics.SeasonCount
	}
	n := 0
	for _, ss := range s.Seasons {
		if ss.SeasonNumber > 0 {
			n++
		}
	}
	return n
}

// IsSingleSeason reports whether the show has exactly one regular season.
// Example (from real Sonarr responses): Breaking Bad -> seasonCount=5 -> false;
// Metástasis -> seasonCount=1 -> true.
func (s *Series) IsSingleSeason() bool {
	return s.RegularSeasonCount() == 1
}

// Client talks to a Sonarr v3 instance using the X-Api-Key header.
type Client struct {
	BaseURL   string
	APIKey    string
	UserAgent string
	HTTP      *http.Client
}

// New returns a Client with sensible defaults.
func New(baseURL, apiKey, userAgent string) *Client {
	return &Client{
		BaseURL:   strings.TrimRight(baseURL, "/"),
		APIKey:    apiKey,
		UserAgent: userAgent,
		HTTP:      &http.Client{Timeout: 30 * time.Second},
	}
}

// Lookup calls GET /api/v3/series/lookup?term=... and returns the raw
// results in Sonarr's ranked order (most relevant first).
func (c *Client) Lookup(ctx context.Context, term string) ([]Series, error) {
	u := fmt.Sprintf("%s/api/v3/series/lookup?term=%s", c.BaseURL, url.QueryEscape(term))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", c.APIKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sonarr lookup: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("sonarr lookup: unauthorized (check SONARR_API_KEY)")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sonarr lookup status %d", resp.StatusCode)
	}

	var out []Series
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode lookup: %w", err)
	}
	return out, nil
}

// Ping verifies credentials by calling /api/v3/system/status.
func (c *Client) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v3/system/status", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", c.APIKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("sonarr ping: unauthorized")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sonarr ping status %d", resp.StatusCode)
	}
	return nil
}

// BestMatch picks the most plausible Sonarr lookup hit for a given ranking
// title. Sonarr returns many loosely-related shows (searching "Breaking
// Bad" yields "Breaking Italy", "Prison Break", etc. per live API data),
// so we need signals beyond Sonarr's own ordering.
//
// Scoring combines, in order of dominance:
//
//  1. Required: the result has a tvdbId. Results without one cannot be fed
//     to Sonarr's Custom List anyway.
//  2. Title similarity to the query. Exact normalized match = 1.0; slug
//     equality = 0.95; else token-Jaccard score over the normalized title
//     and every alternateTitle. A strong title match is the single biggest
//     contributor.
//  3. Anime provenance. +0.30 when originalLanguage.Name == "Japanese"
//     (per OpenAPI spec, this is TVDB metadata, not a user-set tag).
//     +0.10 when the Genres array contains "Anime". +0.05 when
//     seriesType == "anime" (usually unset on lookup results).
//  4. Popularity: +0.05 if ratings.votes >= 100 (filters out obscure
//     fake-looking entries with zero votes).
//  5. Sonarr native ordering: small tiebreaker (-0.0001 * index).
//
// The function returns the best candidate or nil if none have a tvdbId.
func BestMatch(query string, results []Series) *Series {
	if len(results) == 0 {
		return nil
	}
	qNorm := normalizeTitle(query)
	qSlug := slugify(query)
	qTokens := tokenSet(qNorm)

	type scored struct {
		idx   int
		score float64
		s     *Series
	}
	var best *scored
	for i := range results {
		r := &results[i]
		if r.TvdbID <= 0 {
			continue
		}

		// Title similarity — highest of exact/slug/token-jaccard across
		// the canonical title, sortTitle, slug, and every alternate title.
		candidates := []string{r.Title, r.SortTitle, r.TitleSlug}
		for _, alt := range r.AlternateTitles {
			candidates = append(candidates, alt.Title)
		}
		var titleScore float64
		for _, c := range candidates {
			if c == "" {
				continue
			}
			cn := normalizeTitle(c)
			if cn == qNorm {
				titleScore = max64(titleScore, 1.0)
				continue
			}
			if slugify(c) == qSlug {
				titleScore = max64(titleScore, 0.95)
				continue
			}
			titleScore = max64(titleScore, jaccard(qTokens, tokenSet(cn)))
		}

		score := titleScore
		if r.IsJapaneseOriginal() {
			score += 0.30
		}
		if r.HasAnimeGenre() {
			score += 0.10
		}
		if r.SeriesType == "anime" {
			score += 0.05
		}
		if r.Ratings.Votes >= 100 {
			score += 0.05
		}
		score -= float64(i) * 0.0001

		if best == nil || score > best.score {
			best = &scored{idx: i, score: score, s: r}
		}
	}
	if best == nil {
		return nil
	}
	return best.s
}

// normalizeTitle lowercases, strips diacritics/punctuation, and collapses
// whitespace. Used for comparing ranking-post titles to Sonarr titles.
func normalizeTitle(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	b.Grow(len(s))
	lastSpace := true
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastSpace = false
		case r == '-' || r == '_' || r == '!' || r == '?' || r == ':' || r == '.' || r == ',' || r == '\'' || r == '"' || r == '’' || r == '“' || r == '”' || unicode.IsSpace(r):
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// slugify produces a hyphenated, lowercase slug for equality checks against
// Sonarr's titleSlug field.
func slugify(s string) string {
	return strings.ReplaceAll(normalizeTitle(s), " ", "-")
}

// tokenSet splits a normalized title into a set of word tokens.
func tokenSet(s string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, tok := range strings.Fields(s) {
		out[tok] = struct{}{}
	}
	return out
}

// jaccard is the standard set-intersection-over-union similarity.
func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if _, ok := b[k]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func max64(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
