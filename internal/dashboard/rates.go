/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.5.0
 */

package dashboard

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"
)

// throughputTypes is the fixed wire order of the stacked throughput series.
var throughputTypes = []string{"input", "output", "cache_read", "cache_creation"}

// BuildRates assembles /api/usage/rates: per-bucket weighted speed by model
// group, whole-window speed KPIs, and per-bucket throughput by token type.
func BuildRates(ctx context.Context, db *sql.DB, c *Classifier, w TimeWindow, rng string, client Client) (RatesResponse, error) {
	spec, err := w.ResolveRates(rng)
	if err != nil {
		return RatesResponse{}, err
	}
	resp := RatesResponse{Range: spec.Range, BucketInterval: spec.IntervalLabel}

	// ── 生成速度:按 (桶, 组) 合并分子分母后再除(加权平均可无损合并) ──
	speedRows, err := QuerySpeedBuckets(ctx, db, client, spec.Start, spec.End)
	if err != nil {
		return resp, err
	}
	type cellKey struct {
		idx   int
		group string
	}
	type cellAgg struct {
		out, dur int64
	}
	cells := make(map[cellKey]*cellAgg)
	groupOut := make(map[string]int64)
	for _, r := range speedRows {
		g := c.Classify(r.Model)
		if g == "" {
			continue
		}
		idx := spec.BucketIndex(r.Hour)
		if idx < 0 {
			continue
		}
		k := cellKey{idx: idx, group: g}
		a := cells[k]
		if a == nil {
			a = &cellAgg{}
			cells[k] = a
		}
		a.out += r.OutTokens
		a.dur += r.DurMs
		groupOut[g] += r.OutTokens
	}

	groups := make([]string, 0, len(groupOut))
	for g := range groupOut {
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groupOut[groups[i]] != groupOut[groups[j]] {
			return groupOut[groups[i]] > groupOut[groups[j]]
		}
		return groups[i] < groups[j]
	})

	speedPoints := make([]RatesPoint, 0, spec.Count)
	for i := 0; i < spec.Count; i++ {
		bucketStart := spec.Start.Add(time.Duration(i) * spec.Interval)
		values := make(map[string]float64, len(groups))
		for _, g := range groups {
			if a, ok := cells[cellKey{idx: i, group: g}]; ok && a.dur > 0 {
				values[g] = float64(a.out) * 1000 / float64(a.dur)
			}
		}
		speedPoints = append(speedPoints, ratesPointAt(bucketStart, spec.Interval, w.Loc, values))
	}

	cur, err := QuerySpeedWindow(ctx, db, client, spec.Start, spec.End)
	if err != nil {
		return resp, err
	}
	prevStart := spec.Start.Add(-time.Duration(spec.Count) * spec.Interval)
	prev, err := QuerySpeedWindow(ctx, db, client, prevStart, spec.Start)
	if err != nil {
		return resp, err
	}
	resp.Speed = SpeedBlock{
		Groups:   groups,
		Points:   speedPoints,
		Current:  windowTokPerSec(cur),
		Previous: windowTokPerSec(prev),
	}

	// ── 吞吐率:小时行落桶累加,末桶按实际流逝分钟归一 ──
	thrRows, err := QueryThroughputBuckets(ctx, db, client, spec.Start, spec.End)
	if err != nil {
		return resp, err
	}
	thrCells := make([]throughputBucketRow, spec.Count)
	for _, r := range thrRows {
		idx := spec.BucketIndex(r.Hour)
		if idx < 0 {
			continue
		}
		thrCells[idx].In += r.In
		thrCells[idx].Out += r.Out
		thrCells[idx].CacheRead += r.CacheRead
		thrCells[idx].CacheCreation += r.CacheCreation
	}
	thrPoints := make([]RatesPoint, 0, spec.Count)
	for i := 0; i < spec.Count; i++ {
		bucketStart := spec.Start.Add(time.Duration(i) * spec.Interval)
		mins := spec.Interval.Minutes()
		if elapsed := spec.End.Sub(bucketStart); elapsed < spec.Interval {
			mins = elapsed.Minutes()
			if mins < 1 {
				mins = 1 // 桶刚开始时避免分母趋零导致数值爆炸
			}
		}
		values := map[string]float64{
			"input":          float64(thrCells[i].In) / mins,
			"output":         float64(thrCells[i].Out) / mins,
			"cache_read":     float64(thrCells[i].CacheRead) / mins,
			"cache_creation": float64(thrCells[i].CacheCreation) / mins,
		}
		thrPoints = append(thrPoints, ratesPointAt(bucketStart, spec.Interval, w.Loc, values))
	}
	resp.Throughput = ThroughputBlock{Types: throughputTypes, Points: thrPoints}
	return resp, nil
}

// windowTokPerSec converts a window numerator/denominator into tok/s;
// nil when the window has no usable requests.
func windowTokPerSec(sw speedWindow) *float64 {
	if sw.DurMs <= 0 {
		return nil
	}
	v := float64(sw.OutTokens) * 1000 / float64(sw.DurMs)
	return &v
}

// ratesPointAt renders one bucket: RFC3339 UTC ts + local display label.
// Sub-day buckets label as "HH:00", switching to "M/D" at local midnight so
// 48h charts keep day context; day buckets always label "M/D".
func ratesPointAt(bucketStart time.Time, interval time.Duration, loc *time.Location, values map[string]float64) RatesPoint {
	local := bucketStart.In(loc)
	var label string
	if interval >= 24*time.Hour || local.Hour() == 0 {
		label = fmt.Sprintf("%d/%d", int(local.Month()), local.Day())
	} else {
		label = fmt.Sprintf("%02d:00", local.Hour())
	}
	return RatesPoint{
		Ts:     bucketStart.UTC().Format(time.RFC3339),
		Label:  label,
		Values: values,
	}
}
