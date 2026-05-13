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

type CacheBlock struct {
	HitRate    float64 `json:"hit_rate"` // 0..1; cacheRead / (cacheRead + input)
	HitTokens  int64   `json:"hit_tokens"`
	MissTokens int64   `json:"miss_tokens"`
}

type ModelBlock struct {
	Family      string  `json:"family"`
	Requests    int64   `json:"requests"`
	TokensIn    int64   `json:"tokens_in"`
	TokensOut   int64   `json:"tokens_out"`
	CacheTokens int64   `json:"cache_tokens"`
	Cost        float64 `json:"cost"`
	Share       float64 `json:"share"`
}

// TrendsResponse → GET /api/usage/trends?range=
type TrendsResponse struct {
	Range  string        `json:"range"`
	Points []TrendsPoint `json:"points"`
}

type TrendsPoint struct {
	Date   string `json:"date"`
	Label  string `json:"label"`
	Opus   int64  `json:"opus"`
	Sonnet int64  `json:"sonnet"`
	Haiku  int64  `json:"haiku"`
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

// Family constants — keep in sync with the SQL CASE expressions.
const (
	FamilyOpus   = "opus"
	FamilySonnet = "sonnet"
	FamilyHaiku  = "haiku"
	FamilyOther  = "other"
)
