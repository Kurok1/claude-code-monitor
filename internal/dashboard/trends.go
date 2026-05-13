package dashboard

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// BuildTrends runs the trends query and pivots the rows into the response shape.
// Bucket order matches the design: day → 14 buckets, week → 12 buckets, month → 12 buckets.
func BuildTrends(ctx context.Context, db *sql.DB, w TimeWindow, rng string) (TrendsResponse, error) {
	grain, start, count, err := trendsParams(w, rng)
	if err != nil {
		return TrendsResponse{}, err
	}

	rows, err := QueryTrends(ctx, db, w, grain, start)
	if err != nil {
		return TrendsResponse{}, err
	}

	// Index rows by (bucket, family) → tokens. The `other` family is filtered
	// here per the design decision (low-contrast in stacked area).
	type key struct {
		bucket time.Time
		family string
	}
	idx := map[key]int64{}
	for _, r := range rows {
		if r.Family == FamilyOther {
			continue
		}
		idx[key{bucket: r.Bucket.UTC(), family: r.Family}] = r.Tokens
	}

	points := make([]TrendsPoint, 0, count)
	d := start.In(w.Loc)
	for i := 0; i < count; i++ {
		bucketUTC := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC) // DuckDB DATE
		date, label := formatBucket(d, grain)
		points = append(points, TrendsPoint{
			Date:   date,
			Label:  label,
			Opus:   idx[key{bucketUTC, FamilyOpus}],
			Sonnet: idx[key{bucketUTC, FamilySonnet}],
			Haiku:  idx[key{bucketUTC, FamilyHaiku}],
		})
		d = advance(d, grain)
	}

	return TrendsResponse{Range: rng, Points: points}, nil
}

func trendsParams(w TimeWindow, rng string) (grain string, start time.Time, count int, err error) {
	switch rng {
	case "day":
		return "day", w.DayTrendStartUTC, 14, nil
	case "week":
		return "week", w.WeekTrendStartUTC, 12, nil
	case "month":
		return "month", w.MonthTrendStartUTC, 12, nil
	default:
		return "", time.Time{}, 0, fmt.Errorf("invalid range %q: want day|week|month", rng)
	}
}

func advance(d time.Time, grain string) time.Time {
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

// formatBucket returns (date, label) for one bucket.
// date: machine-readable key — YYYY-MM-DD (day/week start) or YYYY-MM (month)
// label: human-friendly — "M/D" (day/week) or "M月" (month)
func formatBucket(d time.Time, grain string) (date, label string) {
	switch grain {
	case "month":
		return fmt.Sprintf("%04d-%02d", d.Year(), d.Month()),
			fmt.Sprintf("%d月", int(d.Month()))
	default:
		return fmt.Sprintf("%04d-%02d-%02d", d.Year(), d.Month(), d.Day()),
			fmt.Sprintf("%d/%d", int(d.Month()), d.Day())
	}
}
