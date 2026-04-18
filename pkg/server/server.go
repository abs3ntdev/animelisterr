package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/abs3ntdev/animelisterr/pkg/refresher"
	"github.com/abs3ntdev/animelisterr/pkg/store"
)

// Server exposes the Sonarr custom list and some operator endpoints.
type Server struct {
	Log       *slog.Logger
	Store     *store.Store
	Refresher *refresher.Refresher
	// MaxSeasonCount caps how many regular seasons a show may have to be
	// included in /sonarr/list. Sourced from MAX_SEASON_COUNT at startup.
	MaxSeasonCount int
	// TopN caps how many of the current week's ranked entries are
	// considered for the list / reported on /status/current. Sourced from
	// TOP_N at startup.
	TopN int
}

// Routes returns a ready-to-serve http.Handler.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/sonarr/list", s.handleSonarrList)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/status/current", s.handleStatusCurrent)
	mux.HandleFunc("/refresh", s.handleRefresh)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return logMiddleware(s.Log, mux)
}

// sonarrListItem is the JSON shape Sonarr's Custom Import List expects.
// See Sonarr's CustomAPIResource.cs: title, tvdbId, tmdbId, imdbId.
type sonarrListItem struct {
	Title  string `json:"title"`
	TvdbID int    `json:"tvdbId"`
	TmdbID int    `json:"tmdbId,omitempty"`
	ImdbID string `json:"imdbId,omitempty"`
}

// handleSonarrList returns the current-week qualifying anime (top 10,
// regular_season_count <= MaxSeasonCount, known TVDB ID) as a JSON array.
func (s *Server) handleSonarrList(w http.ResponseWriter, r *http.Request) {
	items, _, err := s.Store.CurrentSonarrList(r.Context(), s.TopN, s.MaxSeasonCount)
	if err != nil {
		s.Log.Error("sonarr list query", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]sonarrListItem, 0, len(items))
	for _, it := range items {
		out = append(out, sonarrListItem{
			Title:  it.Title,
			TvdbID: it.TvdbID,
			TmdbID: it.TmdbID,
			ImdbID: it.ImdbID,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleStatus reports the refresher's last-run timestamp and error.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Refresher.Status())
}

// debugEntry is what /status/current returns per ranking row. It wraps
// the DB's DebugEntry with a derived Qualifying flag so operators can see
// at a glance whether each entry will end up in /sonarr/list.
type debugEntry struct {
	store.DebugEntry
	Qualifying bool `json:"qualifying"`
}

// handleStatusCurrent returns full debug info about the current week's
// entries so an operator can see which ones are filtered out and why.
func (s *Server) handleStatusCurrent(w http.ResponseWriter, r *http.Request) {
	entries, ranking, err := s.Store.CurrentDebug(r.Context(), s.TopN)
	if err != nil {
		s.Log.Error("debug query", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]debugEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, debugEntry{
			DebugEntry: e,
			Qualifying: isQualifying(e, s.MaxSeasonCount),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ranking":          ranking,
		"max_season_count": s.MaxSeasonCount,
		"top_n":            s.TopN,
		"entries":          out,
	})
}

// isQualifying mirrors the SQL predicate in Store.CurrentSonarrList so
// /status/current and /sonarr/list stay in sync.
func isQualifying(e store.DebugEntry, maxSeasons int) bool {
	if e.TvdbID == nil || *e.TvdbID <= 0 {
		return false
	}
	if maxSeasons > 0 && e.RegularSeasonCount > maxSeasons {
		return false
	}
	return true
}

// handleRefresh triggers an immediate refresh. Forces a re-scrape even if
// the current week is already marked complete, so operators can
// deliberately re-run after fixing title-match issues or similar.
func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err := s.Refresher.RunForce(ctx); err != nil {
		s.Log.Error("manual refresh", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, s.Refresher.Status())
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// logMiddleware emits a one-line access log per request.
func logMiddleware(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		log.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"dur", time.Since(start).String(),
			"remote", r.RemoteAddr,
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
