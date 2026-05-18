package dashboard

// Response types for /api/usage/*. JSON tags use snake_case to match
// the frontend wire contract.

// SnapshotResponse → GET /api/usage/snapshot?range=
//
// All three KPI blocks (tokens / cost / cache) are scoped to the same
// `range` window (day / week / month). The model breakdown table stays
// all-time — it's a global mix indicator, range-independent by design.
type SnapshotResponse struct {
	UpdatedAt string       `json:"updated_at"`
	Range     string       `json:"range"`
	Tokens    TokensBlock  `json:"tokens"`
	Cost      CostBlock    `json:"cost"`
	Cache     CacheBlock   `json:"cache"`
	Models    []ModelBlock `json:"models"`
}

type TokensBlock struct {
	In        int64   `json:"in"`
	Out       int64   `json:"out"`
	Total     int64   `json:"total"`      // input + output + cacheRead + cacheCreation
	PrevTotal int64   `json:"prev_total"` // previous same-length window
	Sparkline []int64 `json:"sparkline"`  // length matches range bucket count (14/12/12)
}

type CostBlock struct {
	Total     float64   `json:"total"`
	PrevTotal float64   `json:"prev_total"`
	Sparkline []float64 `json:"sparkline"`
}

// CacheBlock describes prompt-cache efficacy in the current window.
//
//   - HitRate = cacheRead / (cacheRead + cacheCreation). Null when both
//     are zero (no caching used in this window) — frontend renders "N/A".
//   - ReadTokens     = tokens served from cache (hits).
//   - CreationTokens = tokens written into cache (cache writes / refreshes).
type CacheBlock struct {
	HitRate        *float64 `json:"hit_rate"`
	ReadTokens     int64    `json:"read_tokens"`
	CreationTokens int64    `json:"creation_tokens"`
}

// ModelBlock is one row of the breakdown table, keyed by the classifier
// group (e.g. "opus-4.7", "sonnet-4.6", "deepseek-v3"). Order in the
// response is by total tokens descending so the busiest group sorts first.
type ModelBlock struct {
	Group       string  `json:"group"`
	Requests    int64   `json:"requests"`
	TokensIn    int64   `json:"tokens_in"`
	TokensOut   int64   `json:"tokens_out"`
	CacheTokens int64   `json:"cache_tokens"`
	Cost        float64 `json:"cost"`
	Share       float64 `json:"share"`
}

// TrendsResponse → GET /api/usage/trends?range=
//
// `Groups` enumerates the legend order (descending by total tokens across
// the window). `Points[i].Values[g]` is the bucket value for group `g`;
// groups absent from a bucket are simply missing from the map (frontend
// treats them as zero).
type TrendsResponse struct {
	Range  string        `json:"range"`
	Groups []string      `json:"groups"`
	Points []TrendsPoint `json:"points"`
}

type TrendsPoint struct {
	Date   string           `json:"date"`
	Label  string           `json:"label"`
	Values map[string]int64 `json:"values"`
}

// RankingsResponse → GET /api/usage/rankings?since=
type RankingsResponse struct {
	Since  string      `json:"since"`
	Tools  []ToolRank  `json:"tools"`
	Skills []SkillRank `json:"skills"`
}

type ToolRank struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

type SkillRank struct {
	Name        string `json:"name"`
	Activations int64  `json:"activations"`
}
