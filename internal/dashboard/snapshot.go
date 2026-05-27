package dashboard

import (
	"context"
	"database/sql"
	"sort"
	"time"
)

// BuildSnapshot assembles the snapshot response for the given range.
// Queries are sequential — DuckDB MaxOpenConns=1 makes parallelism pointless.
func BuildSnapshot(ctx context.Context, db *sql.DB, c *Classifier, w TimeWindow, rng string) (SnapshotResponse, error) {
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

	cacheRead, cacheCreation, err := QueryPeriodCache(ctx, db, spec.CurrentStart, spec.CurrentEnd)
	if err != nil {
		return resp, err
	}

	curRequests, err := QueryPeriodRequests(ctx, db, spec.CurrentStart, spec.CurrentEnd)
	if err != nil {
		return resp, err
	}
	prevRequests, err := QueryPeriodRequests(ctx, db, spec.PreviousStart, spec.PreviousEnd)
	if err != nil {
		return resp, err
	}
	requestBuckets, err := QueryRequestsSparkline(ctx, db, w, spec.SparklineGrain,
		spec.SparklineStart, spec.PeriodEnd)
	if err != nil {
		return resp, err
	}

	modelTok, err := QueryModelTokens(ctx, db)
	if err != nil {
		return resp, err
	}
	modelC, err := QueryModelCost(ctx, db)
	if err != nil {
		return resp, err
	}
	modelR, err := QueryModelRequests(ctx, db)
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
		HitRate:        cacheHitRate(cacheRead, cacheCreation),
		ReadTokens:     cacheRead,
		CreationTokens: cacheCreation,
	}
	resp.Requests = RequestsBlock{
		Total:     curRequests,
		PrevTotal: prevRequests,
		Sparkline: fillTokensSparkline(requestBuckets, spec, w.Loc),
	}
	resp.Models = mergeModelGroups(c, modelTok, modelC, modelR)
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

// cacheHitRate returns the ratio of cache reads to total cache-touched
// tokens, or nil when no cache activity exists in the window.
func cacheHitRate(read, creation int64) *float64 {
	denom := read + creation
	if denom == 0 {
		return nil
	}
	v := float64(read) / float64(denom)
	return &v
}

// mergeModelGroups outer-joins the three per-model series by classifier
// group, computes share, drops empty groups, and orders by total tokens
// descending (busiest first). Ties break alphabetically for stable output.
func mergeModelGroups(c *Classifier, tok []modelTokens, costs []modelCost, reqs []modelRequests) []ModelBlock {
	type acc struct {
		tokensIn, tokensOut, cacheTokens int64
		cost                             float64
		requests                         int64
	}
	by := map[string]*acc{}
	get := func(g string) *acc {
		a, ok := by[g]
		if !ok {
			a = &acc{}
			by[g] = a
		}
		return a
	}
	for _, r := range tok {
		g := c.Classify(r.Model)
		if g == "" {
			continue
		}
		a := get(g)
		a.tokensIn += r.TokensIn
		a.tokensOut += r.TokensOut
		a.cacheTokens += r.CacheTokens
	}
	for _, r := range costs {
		g := c.Classify(r.Model)
		if g == "" {
			continue
		}
		get(g).cost += r.Cost
	}
	for _, r := range reqs {
		g := c.Classify(r.Model)
		if g == "" {
			continue
		}
		get(g).requests += r.Requests
	}

	var total int64
	for _, a := range by {
		total += a.tokensIn + a.tokensOut + a.cacheTokens
	}

	out := make([]ModelBlock, 0, len(by))
	for g, a := range by {
		sum := a.tokensIn + a.tokensOut + a.cacheTokens
		if sum == 0 && a.cost == 0 && a.requests == 0 {
			continue
		}
		share := 0.0
		if total > 0 {
			share = float64(sum) / float64(total)
		}
		out = append(out, ModelBlock{
			Group:       g,
			Requests:    a.requests,
			TokensIn:    a.tokensIn,
			TokensOut:   a.tokensOut,
			CacheTokens: a.cacheTokens,
			Cost:        a.cost,
			Share:       share,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		si := out[i].TokensIn + out[i].TokensOut + out[i].CacheTokens
		sj := out[j].TokensIn + out[j].TokensOut + out[j].CacheTokens
		if si != sj {
			return si > sj
		}
		return out[i].Group < out[j].Group
	})
	return out
}
