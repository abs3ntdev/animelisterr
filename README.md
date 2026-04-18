# animelisterr

A small Go service that pulls the weekly
[Anime Corner "Anime of the Week"](https://animecorner.me/category/anime-corner/rankings/anime-of-the-week/)
poll, figures out which of the top-10 entries are single-season TV shows
(according to Sonarr/TheTVDB), and exposes the result as a Sonarr
**Custom List** for import.

## How it works

1. Poll the rankings RSS feed for the most recent
   `<season> <year> Anime Rankings - Week N` post.
2. Scrape the post's ranking table (top N rows — we use 10).
3. For each title, call your Sonarr at `GET /api/v3/series/lookup?term=<title>`
   to resolve TVDB metadata.
4. A show is **single-season** iff `statistics.seasonCount == 1` (falling
   back to counting `seasons[]` entries with `seasonNumber > 0`; season 0
   is TVDB's specials bucket and is ignored).
5. Persist rankings, entries, and anime metadata in Postgres.
   `weeks_included` is tracked over time in `anime_weeks_included`.
6. Serve the qualifying entries at `GET /sonarr/list` in Sonarr's Custom List
   JSON format (`[{title, tvdbId, tmdbId, imdbId}, ...]`).

## Endpoints

| Method | Path | Purpose |
|---|---|---|
| GET  | `/sonarr/list`     | Custom Import List JSON for Sonarr to consume. |
| GET  | `/status`          | Last-refresh timestamp + error (if any). |
| GET  | `/status/current`  | Debug: every entry of the current week with match state. |
| POST | `/refresh`         | Force an immediate refresh. |
| GET  | `/healthz`         | Liveness probe. |

## Configuration

All via environment variables:

| Variable | Default | Notes |
|---|---|---|
| `HTTP_ADDR`        | `:8080` | Listen address |
| `DB_HOST`          | `localhost` | Postgres host |
| `DB_PORT`          | `5432` | |
| `DB_USER`          | `postgres` | |
| `DB_PASS`          | `postgres` | Any characters allowed; URL-encoded internally |
| `DB_NAME`          | `animelisterr` | |
| `DB_SSLMODE`       | `disable` | pgx sslmode |
| `SONARR_URL`       | *required* | e.g. `http://sonarr:8989` |
| `SONARR_API_KEY`   | *required* | From Sonarr -> Settings -> General |
| `FEED_URL`         | anime-corner feed | Override for testing |
| `REFRESH_INTERVAL` | `1h` | Go duration (`30m`, `2h`, etc.) |
| `MAX_SEASON_COUNT` | `1` | Max regular seasons a show may have to qualify. `0` disables the filter. |
| `TOP_N`            | `10` | How many top-ranked entries per week are scraped and considered. `0` = no cap. |
| `DISCORD_WEBHOOK_URL` | *unset* | Optional Discord incoming webhook. When set, posts an embed on new-week detection and refresh failures. |
| `USER_AGENT`       | built-in | HTTP UA for scrapes and API calls |

Only `SONARR_URL` and `SONARR_API_KEY` are strictly required. DB defaults
target a vanilla local Postgres so `go run ./cmd/animelisterr` works
against `postgres/postgres@localhost:5432` out of the box.

## Database migrations

Schema is managed with [tern](https://github.com/jackc/tern) used as a Go
library — migration SQL files are embedded into the binary via `go:embed`
and applied at startup before the main connection pool opens. No separate
tern CLI, `tern.conf`, or entrypoint script is needed.

To add a new migration: drop `NNN_name.sql` into `pkg/migrations/migrate/`
(tern's "up" / `---- create above / drop below ----` / "down" format),
rebuild, and restart. The schema version is tracked in
`public.schema_version`.

## Configure Sonarr to consume the list

1. Sonarr -> Settings -> Import Lists -> **+** -> **Custom List**.
2. **List URL**: `http://animelisterr:8080/sonarr/list`
3. Set quality profile, monitor options, and root folder as you see fit.
4. Test -> Save.

Because the list contains only this week's top 10 qualifying shows, Sonarr
will rotate monitored series as the weekly rankings change. If you enable
*Clean Library Level = Remove & Unmonitor* on the import list, dropped
shows will stop being monitored as soon as they fall out of the top 10.

## Running on Unraid

Point `docker-compose.yml` at the Docker network your Sonarr + Postgres
already share (set `NETWORK_NAME` in `.env`). Create the `animelisterr`
database on your existing Postgres before first run:

```sh
docker exec -it <postgres-container> psql -U postgres -c \
  "CREATE DATABASE animelisterr;"
```

Then `docker compose up -d --build`. Migrations run automatically on boot.
A bundled Postgres service is available in `docker-compose.yml` but
commented out — uncomment it if you don't have an existing Postgres.

## Local development

```sh
# 1. Bring up Postgres locally (defaults match animelisterr's defaults)
docker run -d --name pg -e POSTGRES_PASSWORD=postgres \
  -e POSTGRES_DB=animelisterr -p 5432:5432 postgres:16-alpine

# 2. Set only what's required
export SONARR_URL=http://localhost:8989 SONARR_API_KEY=...

# 3. Run — migrations apply on startup
go run ./cmd/animelisterr
```

Force a refresh and inspect the current week:

```sh
curl -X POST http://localhost:8080/refresh
curl http://localhost:8080/status/current | jq
curl http://localhost:8080/sonarr/list | jq
```
