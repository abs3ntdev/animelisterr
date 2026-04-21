package refresher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/abs3ntdev/animelisterr/pkg/feed"
	"github.com/abs3ntdev/animelisterr/pkg/notify"
	"github.com/abs3ntdev/animelisterr/pkg/scrape"
	"github.com/abs3ntdev/animelisterr/pkg/sonarr"
	"github.com/abs3ntdev/animelisterr/pkg/store"
)

// Refresher pulls the latest ranking and persists it. It is safe to call
// Run concurrently; a mutex serialises refreshes to keep DB traffic sane.
type Refresher struct {
	Log     *slog.Logger
	Feed    *feed.Client
	Scraper *scrape.Scraper
	Sonarr  *sonarr.Client
	Store   *store.Store
	Notify  *notify.Client // may be nil / disabled; never panic

	// TopN is how many of the scraped rankings we care about.
	TopN int

	// MaxSeasonCount is mirrored from config so the new-week Discord
	// notification can flag which entries will actually land in the
	// Sonarr list. Not used for filtering the DB — that still happens at
	// query time.
	MaxSeasonCount int

	mu        sync.Mutex
	lastRun   time.Time
	lastError error
}

// Status is a snapshot used by HTTP status endpoints.
type Status struct {
	LastRun   time.Time `json:"last_run"`
	LastError string    `json:"last_error,omitempty"`
}

// Status returns the most recent run metadata in a thread-safe manner.
func (r *Refresher) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Status{LastRun: r.lastRun}
	if r.lastError != nil {
		s.LastError = r.lastError.Error()
	}
	return s
}

// RunLoop refreshes immediately then on every interval tick until ctx is done.
func (r *Refresher) RunLoop(ctx context.Context, interval time.Duration) {
	// initial run — best-effort, don't abort on error
	if err := r.Run(ctx); err != nil {
		r.Log.Error("initial refresh failed", "err", err)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.Run(ctx); err != nil {
				r.Log.Error("scheduled refresh failed", "err", err)
			}
		}
	}
}

// Run executes a single refresh using the "skip if complete" optimisation:
// when the current week's ranking is already fully recorded, the scrape +
// Sonarr lookups are skipped entirely. Use RunForce to bypass that check.
func (r *Refresher) Run(ctx context.Context) error {
	return r.run(ctx, false)
}

// RunForce always re-scrapes and re-resolves every entry, ignoring whether
// the current week is already complete. Wired to POST /refresh so
// operators can manually re-run a week (for example after fixing a
// misbehaving title match).
func (r *Refresher) RunForce(ctx context.Context) error {
	return r.run(ctx, true)
}

func (r *Refresher) run(ctx context.Context, force bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	post, err := r.Feed.Latest(ctx)
	if err != nil {
		// ErrNoRankingPost is expected between weekly posts (the current
		// week hasn't been published yet, or the last one aged off the
		// feed window). Treat it as a normal no-op: don't log an error,
		// don't notify Discord, don't flip the lastError state. The next
		// hourly tick will try again.
		if errors.Is(err, feed.ErrNoRankingPost) {
			r.Log.Info("no ranking post in feed window yet; will retry next tick")
			r.lastRun = time.Now()
			r.lastError = nil
			return nil
		}
		r.lastError = err
		r.Notify.SendFailure(ctx, "animelisterr: feed fetch failed", err.Error())
		return fmt.Errorf("feed: %w", err)
	}
	r.Log.Info("latest ranking post",
		"slug", post.Slug, "season", post.Season, "year", post.Year, "week", post.Week, "url", post.URL)

	// Decide whether this is a "first time we've seen this week" event
	// before any writes happen. Used to trigger a Discord notification
	// only on genuinely new weeks, not on every hourly re-scrape of the
	// same slug.
	alreadyKnown := false
	if exists, err := r.Store.RankingExists(ctx, post.Slug); err == nil {
		alreadyKnown = exists
	}

	// Skip the scrape + all Sonarr lookups when this week is already
	// fully recorded. Sonarr metadata (TVDB IDs, season counts, etc.) is
	// still refreshed: the upsert path runs on every new week, and on any
	// week that previously had a lookup failure or missing entries.
	// `force` (set by POST /refresh) bypasses the skip so an operator can
	// deliberately re-run a completed week.
	if !force {
		done, err := r.Store.IsRankingComplete(ctx, post.Slug, r.TopN)
		if err != nil {
			r.Log.Warn("ranking completeness check failed; proceeding with re-scrape", "err", err)
		} else if done {
			r.lastRun = time.Now()
			r.lastError = nil
			r.Log.Info("ranking already complete, skipping", "slug", post.Slug)
			return nil
		}
	}

	entries, err := r.Scraper.Top(ctx, post.URL, r.TopN)
	if err != nil {
		r.lastError = err
		r.Notify.SendFailure(ctx, "animelisterr: scrape failed", err.Error())
		return fmt.Errorf("scrape: %w", err)
	}
	r.Log.Info("scraped ranking", "count", len(entries))

	dbEntries := make([]store.Entry, 0, len(entries))
	for _, e := range entries {
		animeID, resolveErr := r.resolveAndUpsert(ctx, e.Title)
		if resolveErr != nil {
			r.Log.Warn("resolve failed", "title", e.Title, "err", resolveErr)
		}
		dbEntries = append(dbEntries, store.Entry{
			Rank:     e.Rank,
			AnimeID:  animeID,
			RawTitle: e.Title,
			Votes:    e.Votes,
		})
	}

	if err := r.Store.RecordRanking(ctx, store.Ranking{
		Slug:        post.Slug,
		Season:      post.Season,
		Year:        post.Year,
		Week:        post.Week,
		PostURL:     post.URL,
		PublishedAt: post.Published,
	}, dbEntries); err != nil {
		r.lastError = err
		r.Notify.SendFailure(ctx, "animelisterr: record ranking failed", err.Error())
		return fmt.Errorf("record ranking: %w", err)
	}

	r.lastRun = time.Now()
	r.lastError = nil
	r.Log.Info("refresh complete", "week", post.Week, "entries", len(dbEntries))

	// Post a Discord embed only when this is the first successful scrape
	// of a genuinely new week. Re-scrapes of an existing week (e.g. via
	// POST /refresh) are silent to avoid double-posting.
	if !alreadyKnown {
		r.notifyNewWeek(ctx, post)
	}
	return nil
}

// notifyNewWeek builds and sends a rich embed summarising the qualifying
// entries just recorded. Looks up the shows from the DB rather than from
// the entries slice so the "qualifying" flag reflects the exact same
// predicate the Sonarr list endpoint uses (max-season-count, known TVDB).
func (r *Refresher) notifyNewWeek(ctx context.Context, post *feed.RankingPost) {
	if !r.Notify.Enabled() {
		return
	}
	items, _, err := r.Store.CurrentSonarrList(ctx, r.TopN, r.MaxSeasonCount)
	if err != nil {
		r.Log.Warn("notify: CurrentSonarrList failed", "err", err)
		return
	}

	// Build a "✓ Title — s:N" line per qualifying show.
	var lines []string
	for _, it := range items {
		lines = append(lines, fmt.Sprintf("• **%s** — %d season(s), tvdb:%d",
			it.Title, it.RegularSeasonCount, it.TvdbID))
	}
	desc := strings.Join(lines, "\n")
	if desc == "" {
		desc = "_No entries qualified for the list this week._"
	}

	e := notify.Embed{
		Title:       fmt.Sprintf("%s %d — Week %d list updated", strings.Title(post.Season), post.Year, post.Week), //nolint:staticcheck
		Description: desc,
		Colour:      notify.ColourSuccess,
		URL:         post.URL,
		Fields: []notify.EmbedField{
			{Name: "Qualifying", Value: fmt.Sprintf("%d of %d", len(items), r.TopN), Inline: true},
			{Name: "Max seasons", Value: fmt.Sprintf("%d", r.MaxSeasonCount), Inline: true},
		},
		Footer: &notify.EmbedFooter{Text: "animelisterr"},
	}
	r.Notify.Send(ctx, e)
}

// resolveAndUpsert looks up a title via Sonarr, picks the best match, and
// upserts the anime row. It always returns a DB id — a placeholder row is
// created when the lookup fails so the ranking_entries FK is satisfied.
//
// The Sonarr lookup query is season-stripped ("Foo Season 2" -> "Foo")
// because TVDB stores every season of a franchise under one series record.
// Searching the raw sequel title produces lower-quality results; the base
// title reliably hits the correct record in the top positions.
func (r *Refresher) resolveAndUpsert(ctx context.Context, title string) (int64, error) {
	searchTerm := sonarr.StripSeasonSuffix(title)
	results, err := r.Sonarr.Lookup(ctx, searchTerm)
	if err != nil {
		a := store.Anime{
			Title:            title,
			MetadataResolved: false,
			MetadataError:    ptr(err.Error()),
		}
		id, upErr := r.Store.UpsertAnime(ctx, a)
		if upErr != nil {
			return 0, fmt.Errorf("upsert placeholder: %w", upErr)
		}
		return id, err
	}

	// Pass the original (unstripped) title to BestMatch — it applies its
	// own season-aware normalization on both sides of the comparison.
	best := sonarr.BestMatch(title, results)
	if best == nil {
		a := store.Anime{
			Title:            title,
			MetadataResolved: false,
			MetadataError:    ptr("no sonarr match"),
		}
		id, upErr := r.Store.UpsertAnime(ctx, a)
		if upErr != nil {
			return 0, fmt.Errorf("upsert placeholder: %w", upErr)
		}
		return id, fmt.Errorf("no sonarr match for %q", title)
	}

	a := store.Anime{
		Title:              best.Title,
		Year:               intPtr(best.Year),
		SortTitle:          strPtr(best.SortTitle),
		TitleSlug:          strPtr(best.TitleSlug),
		Status:             strPtr(best.Status),
		Ended:              best.Ended,
		Runtime:            intPtr(best.Runtime),
		Network:            strPtr(best.Network),
		SeriesType:         strPtr(best.SeriesType),
		RegularSeasonCount: best.RegularSeasonCount(),
		MetadataResolved:   true,
	}
	if best.TvdbID > 0 {
		a.TvdbID = intPtr(best.TvdbID)
	}
	if best.TmdbID > 0 {
		a.TmdbID = intPtr(best.TmdbID)
	}
	if best.TvMazeID > 0 {
		a.TvMazeID = intPtr(best.TvMazeID)
	}
	if best.TvRageID > 0 {
		a.TvRageID = intPtr(best.TvRageID)
	}
	if best.ImdbID != "" {
		a.ImdbID = strPtr(best.ImdbID)
	}
	return r.Store.UpsertAnime(ctx, a)
}

func ptr(s string) *string { return &s }

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func intPtr(i int) *int {
	if i == 0 {
		return nil
	}
	return &i
}
