package dashboard

import (
	"testing"
	"time"
)

func TestNowWindow_Shanghai(t *testing.T) {
	// 2026-05-13 10:30 in Shanghai = 02:30 UTC.
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 5, 13, 10, 30, 0, 0, loc)

	w, err := NowWindow(now, "Asia/Shanghai")
	if err != nil {
		t.Fatalf("NowWindow: %v", err)
	}

	wantTodayStart := time.Date(2026, 5, 12, 16, 0, 0, 0, time.UTC) // SH 00:00 = UTC 16:00 prev day
	if !w.TodayStartUTC.Equal(wantTodayStart) {
		t.Errorf("TodayStartUTC = %v, want %v", w.TodayStartUTC, wantTodayStart)
	}
	if !w.TodayEndUTC.Equal(wantTodayStart.Add(24 * time.Hour)) {
		t.Errorf("TodayEndUTC = %v, want %v", w.TodayEndUTC, wantTodayStart.Add(24*time.Hour))
	}
	if !w.YesterdayStartUTC.Equal(wantTodayStart.Add(-24 * time.Hour)) {
		t.Errorf("YesterdayStartUTC = %v, want %v", w.YesterdayStartUTC, wantTodayStart.Add(-24*time.Hour))
	}

	// NowUTC: 2026-05-13 10:30 SH = 2026-05-13 02:30 UTC.
	wantNow := time.Date(2026, 5, 13, 2, 30, 0, 0, time.UTC)
	if !w.NowUTC.Equal(wantNow) {
		t.Errorf("NowUTC = %v, want %v", w.NowUTC, wantNow)
	}

	// Month start = 2026-05-01 00:00 SH = 2026-04-30 16:00 UTC.
	wantMonth := time.Date(2026, 4, 30, 16, 0, 0, 0, time.UTC)
	if !w.MonthStartUTC.Equal(wantMonth) {
		t.Errorf("MonthStartUTC = %v, want %v", w.MonthStartUTC, wantMonth)
	}

	if !w.DayTrendStartUTC.Equal(wantTodayStart.Add(-13 * 24 * time.Hour)) {
		t.Errorf("DayTrendStartUTC = %v, want today-13d", w.DayTrendStartUTC)
	}

	// Week start: 2026-05-13 is a Wednesday → Monday is 2026-05-11.
	// 12-week trend → starts 11 weeks earlier = 2026-02-23 SH = 2026-02-22 16:00 UTC.
	wantWeekTrend := time.Date(2026, 2, 22, 16, 0, 0, 0, time.UTC)
	if !w.WeekTrendStartUTC.Equal(wantWeekTrend) {
		t.Errorf("WeekTrendStartUTC = %v, want %v", w.WeekTrendStartUTC, wantWeekTrend)
	}

	// 12-month trend: 2025-06-01 00:00 SH = 2025-05-31 16:00 UTC.
	wantMonthTrend := time.Date(2025, 5, 31, 16, 0, 0, 0, time.UTC)
	if !w.MonthTrendStartUTC.Equal(wantMonthTrend) {
		t.Errorf("MonthTrendStartUTC = %v, want %v", w.MonthTrendStartUTC, wantMonthTrend)
	}
}

func TestNowWindow_HeatmapStart(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, loc)
	w, err := NowWindow(now, "Asia/Shanghai")
	if err != nil {
		t.Fatalf("NowWindow: %v", err)
	}
	// Today (SH 2026-05-13) start = UTC 2026-05-12 16:00.
	// Heatmap spans 360 days inclusive → start = today - 359 days.
	wantStart := time.Date(2026, 5, 12, 16, 0, 0, 0, time.UTC).AddDate(0, 0, -359)
	if !w.HeatmapStartUTC.Equal(wantStart) {
		t.Errorf("HeatmapStartUTC = %v, want %v (today-359d)", w.HeatmapStartUTC, wantStart)
	}
}

func TestMondayOf(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	cases := []struct {
		in   time.Time
		want time.Time
	}{
		// Sunday 2026-05-10 → previous Monday 2026-05-04
		{time.Date(2026, 5, 10, 0, 0, 0, 0, loc), time.Date(2026, 5, 4, 0, 0, 0, 0, loc)},
		// Monday 2026-05-11 → itself
		{time.Date(2026, 5, 11, 0, 0, 0, 0, loc), time.Date(2026, 5, 11, 0, 0, 0, 0, loc)},
		// Wednesday 2026-05-13 → 2026-05-11
		{time.Date(2026, 5, 13, 0, 0, 0, 0, loc), time.Date(2026, 5, 11, 0, 0, 0, 0, loc)},
		// Saturday 2026-05-16 → 2026-05-11
		{time.Date(2026, 5, 16, 0, 0, 0, 0, loc), time.Date(2026, 5, 11, 0, 0, 0, 0, loc)},
	}
	for _, c := range cases {
		got := mondayOf(c.in)
		if !got.Equal(c.want) {
			t.Errorf("mondayOf(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestWindowSpec_Resolve(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	// Wed 2026-05-13 10:00 SH = 2026-05-13 02:00 UTC.
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, loc)
	w, _ := NowWindow(now, "Asia/Shanghai")
	nowUTC := time.Date(2026, 5, 13, 2, 0, 0, 0, time.UTC)

	t.Run("day", func(t *testing.T) {
		s, err := w.Resolve("day")
		if err != nil {
			t.Fatal(err)
		}
		// Current: today 00:00 SH → now.
		if !s.CurrentStart.Equal(w.TodayStartUTC) || !s.CurrentEnd.Equal(nowUTC) {
			t.Errorf("day current = [%v, %v), want [TodayStart, NowUTC)", s.CurrentStart, s.CurrentEnd)
		}
		// Previous: full yesterday — [yesterday 00:00, today 00:00).
		if !s.PreviousStart.Equal(w.YesterdayStartUTC) || !s.PreviousEnd.Equal(w.TodayStartUTC) {
			t.Errorf("day previous = [%v, %v), want full yesterday", s.PreviousStart, s.PreviousEnd)
		}
		if !s.PeriodEnd.Equal(w.TodayEndUTC) {
			t.Errorf("day PeriodEnd = %v, want TodayEndUTC %v", s.PeriodEnd, w.TodayEndUTC)
		}
		if s.SparklineGrain != "day" || s.SparklineCount != 14 {
			t.Errorf("day spark = %s × %d, want day × 14", s.SparklineGrain, s.SparklineCount)
		}
	})

	t.Run("week", func(t *testing.T) {
		s, err := w.Resolve("week")
		if err != nil {
			t.Fatal(err)
		}
		// This week's Monday in SH is 2026-05-11 (UTC 2026-05-10 16:00).
		wantMon := time.Date(2026, 5, 10, 16, 0, 0, 0, time.UTC)
		if !s.CurrentStart.Equal(wantMon) || !s.CurrentEnd.Equal(nowUTC) {
			t.Errorf("week current = [%v, %v), want [Mon, Now)", s.CurrentStart, s.CurrentEnd)
		}
		// Previous: full last week — [last Mon, this Mon).
		wantLastMon := wantMon.Add(-7 * 24 * time.Hour)
		if !s.PreviousStart.Equal(wantLastMon) || !s.PreviousEnd.Equal(wantMon) {
			t.Errorf("week previous = [%v, %v), want full last week", s.PreviousStart, s.PreviousEnd)
		}
		if !s.PeriodEnd.Equal(wantMon.Add(7 * 24 * time.Hour)) {
			t.Errorf("week PeriodEnd = %v, want next Mon", s.PeriodEnd)
		}
		if s.SparklineGrain != "week" || s.SparklineCount != 12 {
			t.Errorf("week spark = %s × %d", s.SparklineGrain, s.SparklineCount)
		}
	})

	t.Run("month", func(t *testing.T) {
		s, err := w.Resolve("month")
		if err != nil {
			t.Fatal(err)
		}
		// May 1 SH = 2026-04-30 16:00 UTC; June 1 SH = 2026-05-31 16:00 UTC; Apr 1 SH = 2026-03-31 16:00 UTC.
		wantMay := time.Date(2026, 4, 30, 16, 0, 0, 0, time.UTC)
		wantJun := time.Date(2026, 5, 31, 16, 0, 0, 0, time.UTC)
		wantApr := time.Date(2026, 3, 31, 16, 0, 0, 0, time.UTC)
		if !s.CurrentStart.Equal(wantMay) || !s.CurrentEnd.Equal(nowUTC) {
			t.Errorf("month current = [%v, %v), want [May1, Now)", s.CurrentStart, s.CurrentEnd)
		}
		// Previous: full last month — [Apr 1, May 1).
		if !s.PreviousStart.Equal(wantApr) || !s.PreviousEnd.Equal(wantMay) {
			t.Errorf("month previous = [%v, %v), want full April", s.PreviousStart, s.PreviousEnd)
		}
		if !s.PeriodEnd.Equal(wantJun) {
			t.Errorf("month PeriodEnd = %v, want Jun1", s.PeriodEnd)
		}
		if s.SparklineGrain != "month" || s.SparklineCount != 12 {
			t.Errorf("month spark = %s × %d", s.SparklineGrain, s.SparklineCount)
		}
	})

	t.Run("invalid", func(t *testing.T) {
		if _, err := w.Resolve("year"); err == nil {
			t.Error("expected error for invalid range")
		}
	})
}

func TestSinceStart(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 5, 13, 10, 30, 0, 0, loc)
	w, _ := NowWindow(now, "Asia/Shanghai")

	cases := []struct {
		in      string
		wantTag string
		wantT   time.Time
		wantErr bool
	}{
		{"7d", "7d", w.TodayStartUTC.Add(-7 * 24 * time.Hour), false},
		{"30d", "30d", w.TodayStartUTC.Add(-30 * 24 * time.Hour), false},
		{"all", "all", time.Time{}, false},
		{"", "all", time.Time{}, false},
		{"1y", "", time.Time{}, true},
	}
	for _, c := range cases {
		gotT, gotTag, err := SinceStart(w, c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("SinceStart(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			continue
		}
		if c.wantErr {
			continue
		}
		if gotTag != c.wantTag {
			t.Errorf("SinceStart(%q) tag = %q, want %q", c.in, gotTag, c.wantTag)
		}
		if !gotT.Equal(c.wantT) {
			t.Errorf("SinceStart(%q) t = %v, want %v", c.in, gotT, c.wantT)
		}
	}
}
