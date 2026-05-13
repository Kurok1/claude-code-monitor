package dashboard

import (
	"fmt"
	"time"
)

// TimeWindow holds all boundary instants the dashboard queries need.
// All fields are UTC instants — the timezone we care about (Asia/Shanghai
// by default) is already baked into how the boundaries are computed.
//
// DuckDB columns `ts` are naive TIMESTAMP (microseconds since unix epoch,
// interpreted as UTC). Comparing against UTC instants directly is correct;
// bucketing by local day requires offsetting ts inside SQL (see queries.go).
type TimeWindow struct {
	Loc *time.Location

	// NowUTC is the wall-clock instant the window is rooted at. KPI queries
	// (tokens / cost / cache) use this as their upper bound so partial
	// periods report "since-period-start until now" rather than "full
	// calendar period including future".
	NowUTC time.Time

	// Period anchors — all UTC instants of local 00:00 boundaries.
	TodayStartUTC     time.Time
	TodayEndUTC       time.Time
	YesterdayStartUTC time.Time

	WeekStartUTC     time.Time // this Monday 00:00 (local)
	NextWeekStartUTC time.Time
	LastWeekStartUTC time.Time

	MonthStartUTC     time.Time
	NextMonthStartUTC time.Time
	LastMonthStartUTC time.Time

	// Sparkline / trends backward-window starts (relative to current period start).
	DayTrendStartUTC   time.Time // today_start - 13d
	WeekTrendStartUTC  time.Time // this_week_start - 11w
	MonthTrendStartUTC time.Time // this_month_start - 11mo
}

// NowWindow computes a TimeWindow rooted at the wall-clock `now` interpreted
// in tz. tz must be a valid IANA name (validated at config load).
func NowWindow(now time.Time, tz string) (TimeWindow, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return TimeWindow{}, fmt.Errorf("load location %q: %w", tz, err)
	}
	return windowAt(now.In(loc), loc), nil
}

func windowAt(nowInLoc time.Time, loc *time.Location) TimeWindow {
	todayStart := time.Date(
		nowInLoc.Year(), nowInLoc.Month(), nowInLoc.Day(),
		0, 0, 0, 0, loc,
	)

	weekStart := mondayOf(todayStart)
	monthStart := time.Date(nowInLoc.Year(), nowInLoc.Month(), 1, 0, 0, 0, 0, loc)

	return TimeWindow{
		Loc:                loc,
		NowUTC:             nowInLoc.UTC(),
		TodayStartUTC:      todayStart.UTC(),
		TodayEndUTC:        todayStart.Add(24 * time.Hour).UTC(),
		YesterdayStartUTC:  todayStart.Add(-24 * time.Hour).UTC(),
		WeekStartUTC:       weekStart.UTC(),
		NextWeekStartUTC:   weekStart.AddDate(0, 0, 7).UTC(),
		LastWeekStartUTC:   weekStart.AddDate(0, 0, -7).UTC(),
		MonthStartUTC:      monthStart.UTC(),
		NextMonthStartUTC:  addMonths(monthStart, 1).UTC(),
		LastMonthStartUTC:  addMonths(monthStart, -1).UTC(),
		DayTrendStartUTC:   todayStart.Add(-13 * 24 * time.Hour).UTC(),
		WeekTrendStartUTC:  weekStart.AddDate(0, 0, -7*11).UTC(),
		MonthTrendStartUTC: addMonths(monthStart, -11).UTC(),
	}
}

func mondayOf(t time.Time) time.Time {
	offset := int(t.Weekday()) - 1
	if offset < 0 {
		offset = 6 // Sunday → 6 days back
	}
	return t.AddDate(0, 0, -offset)
}

func addMonths(t time.Time, months int) time.Time {
	return t.AddDate(0, months, 0)
}

// WindowSpec is the resolved current/previous/sparkline boundaries for a
// chosen range. All KPI queries take a WindowSpec and treat the boundaries
// uniformly, so the range-dependent logic lives in one place (Resolve).
//
// Two upper bounds exist on purpose:
//   - CurrentEnd = NowUTC. KPI cards (tokens / cost / cache) report the
//     partial period "from period-start to now" — not the full calendar
//     period — so a 16:00 reading on Monday is honest about being 16/168
//     of the way through the week.
//   - PeriodEnd = the calendar boundary (tomorrow 00:00 / next Monday /
//     next month's day 1). Sparkline queries use this so the last bucket
//     isn't artificially truncated (DuckDB SUM gives the same number
//     either way, but the upper bound stays aligned with the bucket).
//
// Previous covers the FULL prior calendar period (yesterday / last week /
// last month) regardless of how far we are into the current one. The
// "↑/↓ vs 昨日" badge thus reads as "today-so-far vs yesterday-total",
// which matches how users naturally interpret the comparison.
type WindowSpec struct {
	Range          string // day / week / month
	CurrentStart   time.Time
	CurrentEnd     time.Time
	PreviousStart  time.Time
	PreviousEnd    time.Time
	PeriodEnd      time.Time // calendar boundary; for sparkline upper bound
	SparklineStart time.Time
	SparklineGrain string // day / week / month
	SparklineCount int    // 14 / 12 / 12
}

func (w TimeWindow) Resolve(rng string) (WindowSpec, error) {
	// All boundaries are converted to w.Loc on the way out. The instants
	// don't change — time.Time.Equal() / SQL parameter binding only care
	// about the absolute moment — but printf/log output reads as local
	// wall-clock (e.g. "2026-05-13 00:00:00 +0800 CST"), which matches
	// the operator's mental model when debugging.
	switch rng {
	case "day":
		return WindowSpec{
			Range:          "day",
			CurrentStart:   w.TodayStartUTC.In(w.Loc),
			CurrentEnd:     w.NowUTC.In(w.Loc),
			PreviousStart:  w.YesterdayStartUTC.In(w.Loc),
			PreviousEnd:    w.TodayStartUTC.In(w.Loc),
			PeriodEnd:      w.TodayEndUTC.In(w.Loc),
			SparklineStart: w.DayTrendStartUTC.In(w.Loc),
			SparklineGrain: "day",
			SparklineCount: 14,
		}, nil
	case "week":
		return WindowSpec{
			Range:          "week",
			CurrentStart:   w.WeekStartUTC.In(w.Loc),
			CurrentEnd:     w.NowUTC.In(w.Loc),
			PreviousStart:  w.LastWeekStartUTC.In(w.Loc),
			PreviousEnd:    w.WeekStartUTC.In(w.Loc),
			PeriodEnd:      w.NextWeekStartUTC.In(w.Loc),
			SparklineStart: w.WeekTrendStartUTC.In(w.Loc),
			SparklineGrain: "week",
			SparklineCount: 12,
		}, nil
	case "month":
		return WindowSpec{
			Range:          "month",
			CurrentStart:   w.MonthStartUTC.In(w.Loc),
			CurrentEnd:     w.NowUTC.In(w.Loc),
			PreviousStart:  w.LastMonthStartUTC.In(w.Loc),
			PreviousEnd:    w.MonthStartUTC.In(w.Loc),
			PeriodEnd:      w.NextMonthStartUTC.In(w.Loc),
			SparklineStart: w.MonthTrendStartUTC.In(w.Loc),
			SparklineGrain: "month",
			SparklineCount: 12,
		}, nil
	default:
		return WindowSpec{}, fmt.Errorf("invalid range %q: want day|week|month", rng)
	}
}

// SinceStart returns the UTC instant for `since`, plus the validated form.
// `all` returns (zero, "all", nil) → caller treats zero as "no filter".
func SinceStart(w TimeWindow, since string) (time.Time, string, error) {
	switch since {
	case "7d":
		return w.TodayStartUTC.Add(-7 * 24 * time.Hour), "7d", nil
	case "30d":
		return w.TodayStartUTC.Add(-30 * 24 * time.Hour), "30d", nil
	case "all", "":
		return time.Time{}, "all", nil
	default:
		return time.Time{}, "", fmt.Errorf("invalid since %q: want 7d|30d|all", since)
	}
}

// shanghaiOffsetSeconds returns the UTC offset of the configured timezone
// at the given instant. Used to build `ts + INTERVAL N HOUR` for SQL
// bucketing — DuckDB's TIMEZONE() requires TIMESTAMPTZ which we don't have.
//
// Returned as seconds; callers convert to whole hours when interpolating into
// SQL (Asia/Shanghai and the other supported tzs are whole-hour offsets).
func shanghaiOffsetSeconds(w TimeWindow, at time.Time) int {
	_, off := at.In(w.Loc).Zone()
	return off
}
