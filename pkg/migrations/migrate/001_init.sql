-- Initial schema for animelisterr.
-- Tracks anime metadata (from Sonarr /api/v3/series/lookup), weekly
-- rankings scraped from animecorner.me, and the many-to-many link between
-- them. The `current_ranking` view isolates the most recent week; the
-- `anime_weeks_included` view counts how many rankings each anime has
-- appeared in so endpoints can expose that without ad-hoc joins.

CREATE TABLE anime (
    id                   BIGSERIAL PRIMARY KEY,
    tvdb_id              INTEGER      UNIQUE,       -- canonical ID for Sonarr
    tmdb_id              INTEGER,
    tvmaze_id            INTEGER,
    tvrage_id            INTEGER,
    imdb_id              TEXT,
    title                TEXT         NOT NULL,
    sort_title           TEXT,
    title_slug           TEXT,
    year                 INTEGER,
    status               TEXT,                     -- continuing/ended/upcoming
    ended                BOOLEAN      NOT NULL DEFAULT FALSE,
    runtime              INTEGER,
    network              TEXT,
    series_type          TEXT,                     -- standard/daily/anime
    regular_season_count INTEGER      NOT NULL DEFAULT 0,  -- seasons where number > 0
                                                            -- the MAX_SEASON_COUNT filter is
                                                            -- applied at query time, not here
    metadata_resolved    BOOLEAN      NOT NULL DEFAULT FALSE,
    metadata_error       TEXT,
    first_seen_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX anime_tvdb_id_idx    ON anime (tvdb_id) WHERE tvdb_id IS NOT NULL;
CREATE INDEX anime_title_lower_idx ON anime (lower(title));

CREATE TABLE rankings (
    slug         TEXT         PRIMARY KEY,
    season       TEXT         NOT NULL,
    year         INTEGER      NOT NULL,
    week         INTEGER      NOT NULL,
    post_url     TEXT         NOT NULL,
    published_at TIMESTAMPTZ,
    scraped_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    UNIQUE (season, year, week)
);

CREATE TABLE ranking_entries (
    ranking_slug TEXT     NOT NULL REFERENCES rankings(slug) ON DELETE CASCADE,
    rank         INTEGER  NOT NULL,
    anime_id     BIGINT   NOT NULL REFERENCES anime(id)      ON DELETE CASCADE,
    raw_title    TEXT     NOT NULL,
    votes        TEXT,
    PRIMARY KEY (ranking_slug, rank)
);

CREATE INDEX ranking_entries_anime_id_idx ON ranking_entries (anime_id);

-- current_ranking resolves to the most recently scraped week.
CREATE VIEW current_ranking AS
SELECT *
FROM rankings
ORDER BY year DESC, week DESC, scraped_at DESC
LIMIT 1;

-- anime_weeks_included reports how many distinct rankings each anime has
-- appeared in. Used by the /status and /sonarr/list endpoints.
CREATE VIEW anime_weeks_included AS
SELECT a.id           AS anime_id,
       COUNT(DISTINCT re.ranking_slug) AS weeks_included
FROM anime a
LEFT JOIN ranking_entries re ON re.anime_id = a.id
GROUP BY a.id;

---- create above / drop below ----

DROP VIEW IF EXISTS anime_weeks_included;
DROP VIEW IF EXISTS current_ranking;
DROP TABLE IF EXISTS ranking_entries;
DROP TABLE IF EXISTS rankings;
DROP TABLE IF EXISTS anime;
