package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store wraps a pgx connection pool with the queries animelisterr needs.
type Store struct {
	Pool *pgxpool.Pool
}

// New opens a pgx pool using the provided DSN.
func New(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse db config: %w", err)
	}
	cfg.MaxConns = 8
	cfg.MinConns = 1
	cfg.MaxConnLifetime = time.Hour

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return &Store{Pool: pool}, nil
}

// Close releases all connections.
func (s *Store) Close() { s.Pool.Close() }

// Anime mirrors a row in the anime table.
type Anime struct {
	ID                 int64
	TvdbID             *int
	TmdbID             *int
	TvMazeID           *int
	TvRageID           *int
	ImdbID             *string
	Title              string
	SortTitle          *string
	TitleSlug          *string
	Year               *int
	Status             *string
	Ended              bool
	Runtime            *int
	Network            *string
	SeriesType         *string
	RegularSeasonCount int
	MetadataResolved   bool
	MetadataError      *string
}

// UpsertAnime inserts or updates an anime by tvdb_id when known, otherwise
// by lowercased title. Returns the primary key.
func (s *Store) UpsertAnime(ctx context.Context, a Anime) (int64, error) {
	if a.TvdbID != nil && *a.TvdbID > 0 {
		const q = `
INSERT INTO anime (
    tvdb_id, tmdb_id, tvmaze_id, tvrage_id, imdb_id,
    title, sort_title, title_slug, year, status, ended,
    runtime, network, series_type,
    regular_season_count,
    metadata_resolved, metadata_error, updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17, now())
ON CONFLICT (tvdb_id) DO UPDATE SET
    tmdb_id              = EXCLUDED.tmdb_id,
    tvmaze_id            = EXCLUDED.tvmaze_id,
    tvrage_id            = EXCLUDED.tvrage_id,
    imdb_id              = EXCLUDED.imdb_id,
    title                = EXCLUDED.title,
    sort_title           = EXCLUDED.sort_title,
    title_slug           = EXCLUDED.title_slug,
    year                 = EXCLUDED.year,
    status               = EXCLUDED.status,
    ended                = EXCLUDED.ended,
    runtime              = EXCLUDED.runtime,
    network              = EXCLUDED.network,
    series_type          = EXCLUDED.series_type,
    regular_season_count = EXCLUDED.regular_season_count,
    metadata_resolved    = EXCLUDED.metadata_resolved,
    metadata_error       = EXCLUDED.metadata_error,
    updated_at           = now()
RETURNING id`
		var id int64
		err := s.Pool.QueryRow(ctx, q,
			a.TvdbID, a.TmdbID, a.TvMazeID, a.TvRageID, a.ImdbID,
			a.Title, a.SortTitle, a.TitleSlug, a.Year, a.Status, a.Ended,
			a.Runtime, a.Network, a.SeriesType,
			a.RegularSeasonCount,
			a.MetadataResolved, a.MetadataError,
		).Scan(&id)
		return id, err
	}

	// no tvdb_id -> store an unresolved placeholder keyed by title, so
	// repeated failures don't duplicate rows
	const findQ = `SELECT id FROM anime WHERE tvdb_id IS NULL AND lower(title) = lower($1) LIMIT 1`
	var id int64
	err := s.Pool.QueryRow(ctx, findQ, a.Title).Scan(&id)
	switch {
	case err == nil:
		const upd = `UPDATE anime SET metadata_resolved = $2, metadata_error = $3, updated_at = now() WHERE id = $1`
		if _, err := s.Pool.Exec(ctx, upd, id, a.MetadataResolved, a.MetadataError); err != nil {
			return 0, err
		}
		return id, nil
	case errors.Is(err, pgx.ErrNoRows):
		const ins = `INSERT INTO anime (title, metadata_resolved, metadata_error) VALUES ($1, $2, $3) RETURNING id`
		err = s.Pool.QueryRow(ctx, ins, a.Title, a.MetadataResolved, a.MetadataError).Scan(&id)
		return id, err
	default:
		return 0, err
	}
}

// Ranking is a weekly header row.
type Ranking struct {
	Slug        string
	Season      string
	Year        int
	Week        int
	PostURL     string
	PublishedAt time.Time
}

// Entry is a single ranking row joined to an anime.
type Entry struct {
	Rank     int
	AnimeID  int64
	RawTitle string
	Votes    string
}

// RecordRanking upserts the ranking header and replaces its entries in a
// single transaction.
func (s *Store) RecordRanking(ctx context.Context, r Ranking, entries []Entry) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	const upsert = `
INSERT INTO rankings (slug, season, year, week, post_url, published_at, scraped_at)
VALUES ($1,$2,$3,$4,$5,$6, now())
ON CONFLICT (slug) DO UPDATE SET
    season       = EXCLUDED.season,
    year         = EXCLUDED.year,
    week         = EXCLUDED.week,
    post_url     = EXCLUDED.post_url,
    published_at = EXCLUDED.published_at,
    scraped_at   = now()`
	if _, err := tx.Exec(ctx, upsert, r.Slug, r.Season, r.Year, r.Week, r.PostURL, nullTime(r.PublishedAt)); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `DELETE FROM ranking_entries WHERE ranking_slug = $1`, r.Slug); err != nil {
		return err
	}

	const insEntry = `INSERT INTO ranking_entries (ranking_slug, rank, anime_id, raw_title, votes) VALUES ($1,$2,$3,$4,$5)`
	for _, e := range entries {
		if _, err := tx.Exec(ctx, insEntry, r.Slug, e.Rank, e.AnimeID, e.RawTitle, e.Votes); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// RankingExists reports whether a rankings row with this slug already
// exists, regardless of how many of its entries have resolved metadata.
// Used to distinguish "first time we've seen this week" from "we're
// re-scraping a week we already know about" for notification purposes.
func (s *Store) RankingExists(ctx context.Context, slug string) (bool, error) {
	var exists bool
	err := s.Pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM rankings WHERE slug = $1)`, slug).Scan(&exists)
	return exists, err
}

// IsRankingComplete reports whether a ranking with this slug has already
// been scraped AND every one of its entries resolved successfully to a
// real TVDB record. A "complete" week can be skipped on subsequent
// refreshes — there is nothing new to fetch. An incomplete week (missing
// entries, or entries whose Sonarr lookup previously failed) will return
// false so the caller re-tries.
func (s *Store) IsRankingComplete(ctx context.Context, slug string, expectedEntries int) (bool, error) {
	const q = `
SELECT
    COUNT(*)                                                             AS total,
    COUNT(*) FILTER (WHERE a.metadata_resolved = TRUE AND a.tvdb_id IS NOT NULL) AS resolved
FROM ranking_entries re
JOIN anime a ON a.id = re.anime_id
WHERE re.ranking_slug = $1`
	var total, resolved int
	if err := s.Pool.QueryRow(ctx, q, slug).Scan(&total, &resolved); err != nil {
		return false, err
	}
	return total == expectedEntries && resolved == expectedEntries, nil
}

// SonarrItem is a row returned to Sonarr's Custom Import List.
type SonarrItem struct {
	Title              string
	TvdbID             int
	ImdbID             string
	TmdbID             int
	Rank               int
	Votes              string
	RawTitle           string
	WeeksIncluded      int
	RegularSeasonCount int
}

// CurrentSonarrList returns the qualifying entries from the most recent
// weekly ranking: the top `topN` rows that also have a known TVDB ID and
// regular_season_count <= maxSeasons. Passing maxSeasons <= 0 disables
// the season-count filter; topN <= 0 disables the ranking cap.
func (s *Store) CurrentSonarrList(ctx context.Context, topN, maxSeasons int) ([]SonarrItem, *Ranking, error) {
	const header = `SELECT slug, season, year, week, post_url, published_at FROM current_ranking`
	var r Ranking
	var pub *time.Time
	err := s.Pool.QueryRow(ctx, header).Scan(&r.Slug, &r.Season, &r.Year, &r.Week, &r.PostURL, &pub)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if pub != nil {
		r.PublishedAt = *pub
	}

	// $2 and $3 filters: when topN / maxSeasons are <= 0 the caller
	// wants no cap / no filter, so the predicate becomes trivially true.
	const q = `
SELECT re.rank, re.raw_title, re.votes,
       a.title, a.tvdb_id, COALESCE(a.imdb_id,''), COALESCE(a.tmdb_id, 0),
       a.regular_season_count,
       COALESCE(w.weeks_included, 0)
FROM ranking_entries re
JOIN anime a ON a.id = re.anime_id
LEFT JOIN anime_weeks_included w ON w.anime_id = a.id
WHERE re.ranking_slug = $1
  AND ($2 <= 0 OR re.rank <= $2)
  AND a.tvdb_id IS NOT NULL
  AND ($3 <= 0 OR a.regular_season_count <= $3)
ORDER BY re.rank`
	rows, err := s.Pool.Query(ctx, q, r.Slug, topN, maxSeasons)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var items []SonarrItem
	for rows.Next() {
		var it SonarrItem
		var tvdbID, tmdbID int
		var weeks int64
		if err := rows.Scan(&it.Rank, &it.RawTitle, &it.Votes, &it.Title, &tvdbID, &it.ImdbID, &tmdbID, &it.RegularSeasonCount, &weeks); err != nil {
			return nil, nil, err
		}
		it.TvdbID = tvdbID
		it.TmdbID = tmdbID
		it.WeeksIncluded = int(weeks)
		items = append(items, it)
	}
	return items, &r, rows.Err()
}

// DebugEntry is a row returned by /status/current — every entry from the
// current week, qualifying or not, with the reason for its status.
type DebugEntry struct {
	Rank               int
	RawTitle           string
	MatchedTitle       *string
	TvdbID             *int
	RegularSeasonCount int
	MetadataResolved   bool
	MetadataError      *string
	WeeksIncluded      int
}

// CurrentDebug returns the top `topN` entries of the current week with
// their resolution state, for operator visibility. Passing topN <= 0
// returns every entry in the ranking.
func (s *Store) CurrentDebug(ctx context.Context, topN int) ([]DebugEntry, *Ranking, error) {
	const header = `SELECT slug, season, year, week, post_url, published_at FROM current_ranking`
	var r Ranking
	var pub *time.Time
	err := s.Pool.QueryRow(ctx, header).Scan(&r.Slug, &r.Season, &r.Year, &r.Week, &r.PostURL, &pub)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if pub != nil {
		r.PublishedAt = *pub
	}

	const q = `
SELECT re.rank, re.raw_title,
       a.title, a.tvdb_id, a.regular_season_count,
       a.metadata_resolved, a.metadata_error,
       COALESCE(w.weeks_included, 0)
FROM ranking_entries re
JOIN anime a ON a.id = re.anime_id
LEFT JOIN anime_weeks_included w ON w.anime_id = a.id
WHERE re.ranking_slug = $1
  AND ($2 <= 0 OR re.rank <= $2)
ORDER BY re.rank`
	rows, err := s.Pool.Query(ctx, q, r.Slug, topN)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var out []DebugEntry
	for rows.Next() {
		var e DebugEntry
		var weeks int64
		if err := rows.Scan(&e.Rank, &e.RawTitle, &e.MatchedTitle, &e.TvdbID, &e.RegularSeasonCount, &e.MetadataResolved, &e.MetadataError, &weeks); err != nil {
			return nil, nil, err
		}
		e.WeeksIncluded = int(weeks)
		out = append(out, e)
	}
	return out, &r, rows.Err()
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
