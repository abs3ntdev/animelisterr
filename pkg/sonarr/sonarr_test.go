package sonarr

import "testing"

// realLookupResults mimics (a trimmed subset of) the response the user
// observed from Sonarr's /api/v3/series/lookup?term=breaking%20bad. It's
// kept small but preserves every field BestMatch/IsSingleSeason touches.
func realLookupResults() []Series {
	return []Series{
		{
			Title: "Breaking Bad", SortTitle: "breaking bad", TitleSlug: "breaking-bad",
			TvdbID: 81189, TmdbID: 1396, ImdbID: "tt0903747", SeriesType: "standard",
			Seasons: []Season{
				{SeasonNumber: 0}, {SeasonNumber: 1}, {SeasonNumber: 2},
				{SeasonNumber: 3}, {SeasonNumber: 4}, {SeasonNumber: 5},
			},
			Statistics: Statistics{SeasonCount: 5},
		},
		{
			Title: "Bradley & Barney Walsh: Breaking Dad", TitleSlug: "bradley-and-barney-walsh-breaking-dad",
			TvdbID: 357539, SeriesType: "standard",
			Seasons:    make([]Season, 8),
			Statistics: Statistics{SeasonCount: 7},
		},
		{
			Title: "Metástasis", TitleSlug: "metastasis",
			TvdbID: 273859, SeriesType: "standard",
			Seasons:    []Season{{SeasonNumber: 0}, {SeasonNumber: 1}},
			Statistics: Statistics{SeasonCount: 1},
		},
		{
			Title: "Prison Break", TitleSlug: "prison-break",
			TvdbID: 360115, SeriesType: "standard",
			Seasons: []Season{
				{SeasonNumber: 0}, {SeasonNumber: 1}, {SeasonNumber: 2},
				{SeasonNumber: 3}, {SeasonNumber: 4}, {SeasonNumber: 5},
			},
			Statistics: Statistics{SeasonCount: 5},
		},
	}
}

func TestIsSingleSeason_FromRealSonarrData(t *testing.T) {
	cases := []struct {
		name string
		s    Series
		want bool
	}{
		{
			"Breaking Bad (5 seasons) is NOT single-season",
			Series{Statistics: Statistics{SeasonCount: 5}},
			false,
		},
		{
			"Metastasis (1 season) IS single-season",
			Series{Statistics: Statistics{SeasonCount: 1}},
			true,
		},
		{
			"No statistics, walk seasons[] fallback: [0,1] -> single",
			Series{Seasons: []Season{{SeasonNumber: 0}, {SeasonNumber: 1}}},
			true,
		},
		{
			"No statistics, walk seasons[] fallback: [0,1,2] -> not single",
			Series{Seasons: []Season{{SeasonNumber: 0}, {SeasonNumber: 1}, {SeasonNumber: 2}}},
			false,
		},
		{
			"Empty -> not single (defensive)",
			Series{},
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.s.IsSingleSeason(); got != tc.want {
				t.Fatalf("IsSingleSeason=%v, want %v (count=%d)", got, tc.want, tc.s.RegularSeasonCount())
			}
		})
	}
}

func TestBestMatch_PicksExactTitleMatch(t *testing.T) {
	results := realLookupResults()
	best := BestMatch("Breaking Bad", results)
	if best == nil {
		t.Fatal("BestMatch returned nil")
	}
	if best.TvdbID != 81189 {
		t.Fatalf("got tvdbId %d (%q), want 81189 (Breaking Bad)", best.TvdbID, best.Title)
	}
}

func TestBestMatch_RejectsWhenNoTvdbID(t *testing.T) {
	results := []Series{{Title: "Something", TvdbID: 0}}
	if got := BestMatch("Something", results); got != nil {
		t.Fatalf("expected nil when no result has tvdbId, got %+v", got)
	}
}

func TestBestMatch_IgnoresIrrelevantResults(t *testing.T) {
	// Simulate the real-world case where Sonarr returns many loosely-
	// related shows. We expect the exact title match to win regardless of
	// order.
	results := realLookupResults()
	// rotate so Breaking Bad is last
	rotated := append([]Series{}, results[1:]...)
	rotated = append(rotated, results[0])
	best := BestMatch("Breaking Bad", rotated)
	if best == nil || best.TvdbID != 81189 {
		var got int
		if best != nil {
			got = best.TvdbID
		}
		t.Fatalf("expected Breaking Bad (81189) regardless of ordering, got tvdbId %d", got)
	}
}

func TestBestMatch_PrefersAnimeOnTie(t *testing.T) {
	// Two candidates with equal title similarity; anime one should win.
	results := []Series{
		{Title: "Show X", TitleSlug: "show-x", TvdbID: 1, SeriesType: "standard",
			Statistics: Statistics{SeasonCount: 1}},
		{Title: "Show X", TitleSlug: "show-x", TvdbID: 2, SeriesType: "anime",
			Statistics: Statistics{SeasonCount: 1}},
	}
	best := BestMatch("Show X", results)
	if best == nil || best.TvdbID != 2 {
		var got int
		if best != nil {
			got = best.TvdbID
		}
		t.Fatalf("expected anime candidate (tvdbId=2), got %d", got)
	}
}

func TestBestMatch_PrefersJapaneseOriginalOnTitleTie(t *testing.T) {
	// Same title on two entries; the Japanese-origin one (TVDB anime
	// record) should win over an unrelated same-named Western show even if
	// the Western show is listed first by Sonarr.
	results := []Series{
		{
			Title: "Witch Hat Atelier", TitleSlug: "witch-hat-atelier",
			TvdbID: 111, SeriesType: "standard",
			OriginalLanguage: Language{Name: "English"},
			Statistics:       Statistics{SeasonCount: 1},
		},
		{
			Title: "Witch Hat Atelier", TitleSlug: "witch-hat-atelier",
			TvdbID: 222, SeriesType: "standard",
			OriginalLanguage: Language{Name: "Japanese"},
			Genres:           []string{"Animation", "Anime"},
			Statistics:       Statistics{SeasonCount: 1},
		},
	}
	best := BestMatch("Witch Hat Atelier", results)
	if best == nil || best.TvdbID != 222 {
		var got int
		if best != nil {
			got = best.TvdbID
		}
		t.Fatalf("expected Japanese-original (tvdbId=222), got %d", got)
	}
}

func TestBestMatch_ExactTitleBeatsJapaneseFuzzyMatch(t *testing.T) {
	// Sanity: perfect title match on a non-anime must still beat a
	// Japanese-language result with only a fuzzy title match. Otherwise
	// the language bonus would overwhelm real matches.
	results := []Series{
		{
			Title: "Breaking Bad", TitleSlug: "breaking-bad",
			TvdbID: 81189, SeriesType: "standard",
			OriginalLanguage: Language{Name: "English"},
			Statistics:       Statistics{SeasonCount: 5},
		},
		{
			Title: "Totally Unrelated Japanese Show", TitleSlug: "totally-unrelated-japanese-show",
			TvdbID: 999, SeriesType: "anime",
			OriginalLanguage: Language{Name: "Japanese"},
			Genres:           []string{"Animation", "Anime"},
			Statistics:       Statistics{SeasonCount: 1},
		},
	}
	best := BestMatch("Breaking Bad", results)
	if best == nil || best.TvdbID != 81189 {
		var got int
		if best != nil {
			got = best.TvdbID
		}
		t.Fatalf("expected exact title match to win (81189), got %d", got)
	}
}

func TestNormalizeTitle(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Re:ZERO -Starting Life in Another World- Season 4", "re zero starting life in another world season 4"},
		{"The Angel Next Door Spoils Me Rotten Season 2", "the angel next door spoils me rotten season 2"},
		{"  Metástasis!  ", "metástasis"},
		{"Gals Can’t Be Kind to Otaku!?", "gals can t be kind to otaku"},
	}
	for _, tc := range cases {
		if got := normalizeTitle(tc.in); got != tc.want {
			t.Errorf("normalizeTitle(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStripSeasonSuffix(t *testing.T) {
	cases := []struct{ in, want string }{
		// Real ranking titles from live scrapes.
		// Trailing punctuation that introduces the season marker is also
		// stripped; this is the intended behaviour because TVDB records
		// also lack the stylistic closing dash (e.g. "Re: ZERO, Starting
		// Life in Another World" has no trailing "-").
		{"Re:ZERO -Starting Life in Another World- Season 4", "Re:ZERO -Starting Life in Another World"},
		{"The Angel Next Door Spoils Me Rotten Season 2", "The Angel Next Door Spoils Me Rotten"},
		{"Classroom of the Elite IV", "Classroom of the Elite"},
		{"Dr. Stone: Science Future Part 3", "Dr. Stone: Science Future"},
		{"Attack on Titan S4", "Attack on Titan"},
		{"Something Cour 2", "Something"},
		// Stacked suffixes collapse
		{"Foo Season 2 Part 2", "Foo"},
		// Titles without suffixes are unchanged
		{"Witch Hat Atelier", "Witch Hat Atelier"},
		{"MARRIAGETOXIN", "MARRIAGETOXIN"},
		{"Daemons of the Shadow Realm", "Daemons of the Shadow Realm"},
		// Edge cases we do NOT want to over-strip
		{"Re:ZERO", "Re:ZERO"},             // contains "Zero" but not trailing
		{"Overlord IV", "Overlord"},        // Roman IV stripped (intentional)
		{"86 EIGHTY-SIX", "86 EIGHTY-SIX"}, // digit-only tokens in the middle kept
	}
	for _, tc := range cases {
		if got := StripSeasonSuffix(tc.in); got != tc.want {
			t.Errorf("StripSeasonSuffix(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBestMatch_SeasonSuffixMatchesBaseRecord(t *testing.T) {
	// Live case from production: "Re:ZERO ... Season 4" must match the
	// single TVDB record for the Re:ZERO franchise (which has
	// seasonCount=4 and is correctly filtered out downstream).
	results := []Series{
		{
			Title: "Re: ZERO, Starting Life in Another World", TitleSlug: "rezero-starting-life-in-another-world",
			TvdbID:           305089,
			OriginalLanguage: Language{Name: "Japanese"},
			Genres:           []string{"Animation", "Anime"},
			Statistics:       Statistics{SeasonCount: 4},
			Ratings:          Ratings{Votes: 50000},
		},
	}
	best := BestMatch("Re:ZERO -Starting Life in Another World- Season 4", results)
	if best == nil || best.TvdbID != 305089 {
		var got int
		if best != nil {
			got = best.TvdbID
		}
		t.Fatalf("expected Re:ZERO TVDB record (305089), got %d", got)
	}
}

func TestBestMatch_RejectsLowSimilarityEvenIfAnime(t *testing.T) {
	// Live failure case from production: a TVDB duplicate-placeholder
	// record for "Diebuster" (Japanese, anime genre) was winning the
	// lookup for "The Angel Next Door Spoils Me Rotten" because the
	// 0.45 anime bonus swamped the near-zero title overlap. The
	// similarity floor must reject it outright.
	results := []Series{
		{
			Title:            "Diebuster (Dupe series entry, remove me. Gunbuster Series has this as Season 2)",
			TitleSlug:        "diebuster-dupe",
			TvdbID:           418275,
			OriginalLanguage: Language{Name: "Japanese"},
			Genres:           []string{"Animation", "Anime"},
			Statistics:       Statistics{SeasonCount: 2},
			Ratings:          Ratings{Votes: 500},
		},
	}
	if best := BestMatch("The Angel Next Door Spoils Me Rotten Season 2", results); best != nil {
		t.Fatalf("expected nil (low similarity), got tvdbId=%d title=%q", best.TvdbID, best.Title)
	}
}

func TestBestMatch_AcceptsLegitimateFuzzyMatch(t *testing.T) {
	// The floor must not reject real matches that only partially
	// overlap. "Dr. Stone: Science Future Part 3" -> "Dr. STONE" should
	// still succeed because after season-strip the query "Dr. Stone:
	// Science Future" and the candidate "Dr. STONE" share the "dr stone"
	// tokens (2/4 = 0.5 jaccard, well above the 0.25 floor).
	results := []Series{
		{
			Title:            "Dr. STONE",
			TitleSlug:        "dr-stone",
			TvdbID:           355774,
			OriginalLanguage: Language{Name: "Japanese"},
			Genres:           []string{"Animation", "Anime"},
			Statistics:       Statistics{SeasonCount: 4},
			Ratings:          Ratings{Votes: 10000},
		},
	}
	best := BestMatch("Dr. Stone: Science Future Part 3", results)
	if best == nil || best.TvdbID != 355774 {
		var got int
		if best != nil {
			got = best.TvdbID
		}
		t.Fatalf("expected Dr. STONE (355774), got %d", got)
	}
}
