package dashboard

import (
	"context"
	"database/sql"
	"time"
)

// BuildSnapshot assembles the snapshot response for the given range.
// Queries are sequential — DuckDB MaxOpenConns=1 makes parallelism pointless.
func BuildSnapshot(ctx context.Context, db *sql.DB, w TimeWindow, rng string) (SnapshotResponse, error) {
	var resp SnapshotResponse
	resp.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	spec, err := w.Resolve(rng)
	if err != nil {
		return resp, err
	}
	resp.Range = spec.Range

	curTokens, err := QueryPeriodTokens(ctx, db, spec.CurrentStart, spec.CurrentEnd)
	if err != nil {
		return resp, err
	}
	prevTokens, err := QueryPeriodTokensTotal(ctx, db, spec.PreviousStart, spec.PreviousEnd)
	if err != nil {
		return resp, err
	}
	tokenBuckets, err := QueryTokensSparkline(ctx, db, w, spec.SparklineGrain,
		spec.SparklineStart, spec.PeriodEnd)
	if err != nil {
		return resp, err
	}

	curCost, err := QueryPeriodCost(ctx, db, spec.CurrentStart, spec.CurrentEnd)
	if err != nil {
		return resp, err
	}
	prevCost, err := QueryPeriodCost(ctx, db, spec.PreviousStart, spec.PreviousEnd)
	if err != nil {
		return resp, err
	}
	costBuckets, err := QueryCostSparkline(ctx, db, w, spec.SparklineGrain,
		spec.SparklineStart, spec.PeriodEnd)
	if err != nil {
		return resp, err
	}

	hit, miss, err := QueryPeriodCache(ctx, db, spec.CurrentStart, spec.CurrentEnd)
	if err != nil {
		return resp, err
	}

	familyTok, err := QueryFamilyTokens(ctx, db)
	if err != nil {
		return resp, err
	}
	familyC, err := QueryFamilyCost(ctx, db)
	if err != nil {
		return resp, err
	}
	familyR, err := QueryFamilyRequests(ctx, db)
	if err != nil {
		return resp, err
	}

	resp.Tokens = TokensBlock{
		In:        curTokens.In,
		Out:       curTokens.Out,
		Total:     curTokens.Total,
		PrevTotal: prevTokens,
		Sparkline: fillTokensSparkline(tokenBuckets, spec, w.Loc),
	}
	resp.Cost = CostBlock{
		Total:     curCost,
		PrevTotal: prevCost,
		Sparkline: fillCostSparkline(costBuckets, spec, w.Loc),
	}
	resp.Cache = CacheBlock{
		HitRate:    cacheHitRate(hit, miss),
		HitTokens:  hit,
		MissTokens: miss,
	}
	resp.Models = mergeModelFamilies(familyTok, familyC, familyR)
	return resp, nil
}

// fillTokensSparkline pads the sparse query result to length spec.SparklineCount
// (zero-filled where data is missing). Bucket starts advance by spec.SparklineGrain.
func fillTokensSparkline(rows []periodBucket, spec WindowSpec, loc *time.Location) []int64 {
	byBucket := map[time.Time]int64{}
	for _, r := range rows {
		byBucket[r.Bucket.UTC()] = r.Total
	}
	out := make([]int64, 0, spec.SparklineCount)
	d := spec.SparklineStart.In(loc)
	for i := 0; i < spec.SparklineCount; i++ {
		key := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC) // DuckDB DATE → UTC midnight
		out = append(out, byBucket[key])
		d = advanceGrain(d, spec.SparklineGrain)
	}
	return out
}

func fillCostSparkline(rows []periodCostBucket, spec WindowSpec, loc *time.Location) []float64 {
	byBucket := map[time.Time]float64{}
	for _, r := range rows {
		byBucket[r.Bucket.UTC()] = r.Cost
	}
	out := make([]float64, 0, spec.SparklineCount)
	d := spec.SparklineStart.In(loc)
	for i := 0; i < spec.SparklineCount; i++ {
		key := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
		out = append(out, byBucket[key])
		d = advanceGrain(d, spec.SparklineGrain)
	}
	return out
}

func advanceGrain(d time.Time, grain string) time.Time {
	switch grain {
	case "day":
		return d.AddDate(0, 0, 1)
	case "week":
		return d.AddDate(0, 0, 7)
	case "month":
		return d.AddDate(0, 1, 0)
	}
	return d
}

func cacheHitRate(hit, miss int64) float64 {
	denom := hit + miss
	if denom == 0 {
		return 0
	}
	return float64(hit) / float64(denom)
}

// mergeModelFamilies outer-joins tokens/cost/requests by family, computes share,
// drops empty families. Stable order: opus, sonnet, haiku, other.
func mergeModelFamilies(tok []familyTokens, costs []familyCost, reqs []familyRequests) []ModelBlock {
	type acc struct {
		tokensIn, tokensOut, cacheTokens int64
		cost                             float64
		requests                         int64
	}
	by := map[string]*acc{}
	get := func(f string) *acc {
		a, ok := by[f]
		if !ok {
			a = &acc{}
			by[f] = a
		}
		return a
	}
	for _, r := range tok {
		a := get(r.Family)
		a.tokensIn = r.TokensIn
		a.tokensOut = r.TokensOut
		a.cacheTokens = r.CacheTokens
	}
	for _, r := range costs {
		get(r.Family).cost = r.Cost
	}
	for _, r := range reqs {
		get(r.Family).requests = r.Requests
	}

	var total int64
	for _, a := range by {
		total += a.tokensIn + a.tokensOut + a.cacheTokens
	}

	order := []string{FamilyOpus, FamilySonnet, FamilyHaiku, FamilyOther}
	out := make([]ModelBlock, 0, len(order))
	for _, f := range order {
		a := by[f]
		if a == nil {
			continue
		}
		sum := a.tokensIn + a.tokensOut + a.cacheTokens
		if sum == 0 && a.cost == 0 && a.requests == 0 {
			continue
		}
		share := 0.0
		if total > 0 {
			share = float64(sum) / float64(total)
		}
		out = append(out, ModelBlock{
			Family:      f,
			Requests:    a.requests,
			TokensIn:    a.tokensIn,
			TokensOut:   a.tokensOut,
			CacheTokens: a.cacheTokens,
			Cost:        a.cost,
			Share:       share,
		})
	}
	return out
}

