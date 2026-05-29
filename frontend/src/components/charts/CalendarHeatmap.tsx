/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.0.0
 */
// GitHub-contributions-style calendar heatmap. Renders one SVG <rect> per
// calendar day, colored by a 5-level quantile bucket of the backend-computed
// composite `score` (level 0 = no activity). Hand-rolled SVG, no chart libs.
import { useMemo, useRef, useState } from 'react';
import type { HeatmapDay } from '../../api/dashboard';
import { formatCurrency, formatTokens } from '../../lib/format';

interface Props {
  days: HeatmapDay[];
}

const CELL = 12;
const GAP = 3;
const STEP = CELL + GAP;
const TOP = 18; // month-label band height
const LEFT = 26; // weekday-label gutter width
const WEEKDAY_LABELS = ['一', '', '三', '', '五', '', '日']; // Monday-first

// Parse "YYYY-MM-DD" as a LOCAL calendar date (avoid the UTC shift that
// `new Date("YYYY-MM-DD")` applies).
function parseDay(s: string): Date {
  const [y, m, d] = s.split('-').map(Number);
  return new Date(y, m - 1, d);
}

// Monday=0 … Sunday=6.
function weekdayMon(d: Date): number {
  return (d.getDay() + 6) % 7;
}

// Linear-interpolated quantile of a pre-sorted ascending array.
function quantile(sorted: number[], q: number): number {
  if (sorted.length === 0) return 0;
  const pos = (sorted.length - 1) * q;
  const base = Math.floor(pos);
  const rest = pos - base;
  return base + 1 < sorted.length
    ? sorted[base] + rest * (sorted[base + 1] - sorted[base])
    : sorted[base];
}

interface Cell {
  i: number;
  day: HeatmapDay;
  col: number;
  row: number;
}

export function CalendarHeatmap({ days }: Props) {
  const wrapRef = useRef<HTMLDivElement>(null);
  const [hover, setHover] = useState<number | null>(null);

  const { cells, weeks, monthLabels, levelOf } = useMemo(() => {
    // Quartile thresholds over POSITIVE scores; empty days are level 0.
    const positives = days
      .map(d => d.score)
      .filter(s => s > 0)
      .sort((a, b) => a - b);
    const t1 = quantile(positives, 0.25);
    const t2 = quantile(positives, 0.5);
    const t3 = quantile(positives, 0.75);
    const levelOf = (score: number): number => {
      if (score <= 0) return 0;
      if (score <= t1) return 1;
      if (score <= t2) return 2;
      if (score <= t3) return 3;
      return 4;
    };

    if (days.length === 0) {
      return { cells: [] as Cell[], weeks: 0, monthLabels: [] as { col: number; label: string }[], levelOf };
    }

    const firstMonOffset = weekdayMon(parseDay(days[0].date));
    const cells: Cell[] = days.map((day, i) => ({
      i,
      day,
      col: Math.floor((firstMonOffset + i) / 7),
      row: weekdayMon(parseDay(day.date)),
    }));
    const weeks = cells[cells.length - 1].col + 1;

    const monthLabels: { col: number; label: string }[] = [];
    let lastMonth = -1;
    for (const c of cells) {
      const mo = parseDay(c.day.date).getMonth();
      if (mo !== lastMonth) {
        monthLabels.push({ col: c.col, label: `${mo + 1}月` });
        lastMonth = mo;
      }
    }
    return { cells, weeks, monthLabels, levelOf };
  }, [days]);

  const W = LEFT + weeks * STEP + GAP;
  const H = TOP + 7 * STEP;
  const hoverCell = hover != null ? cells[hover] : null;

  return (
    <div className="heatmap-wrap" ref={wrapRef}>
      <svg className="heatmap-svg" width={W} height={H} role="img" aria-label="最近 360 天用量热点图">
        {WEEKDAY_LABELS.map((l, r) =>
          l ? (
            <text key={r} className="heatmap-wd" x={LEFT - 6} y={TOP + r * STEP + CELL - 2} textAnchor="end">
              {l}
            </text>
          ) : null,
        )}
        {monthLabels.map((m, i) => (
          <text key={i} className="heatmap-mo" x={LEFT + m.col * STEP} y={TOP - 6}>
            {m.label}
          </text>
        ))}
        {cells.map(c => (
          <rect
            key={c.i}
            className="heatmap-cell"
            data-level={levelOf(c.day.score)}
            x={LEFT + c.col * STEP}
            y={TOP + c.row * STEP}
            width={CELL}
            height={CELL}
            rx={2}
            onMouseEnter={() => setHover(c.i)}
            onMouseLeave={() => setHover(null)}
          />
        ))}
      </svg>

      {hoverCell && (
        <div
          className="chart-tooltip heatmap-tip"
          data-visible="true"
          style={{ left: LEFT + hoverCell.col * STEP + CELL / 2, top: TOP + hoverCell.row * STEP }}
        >
          <div className="chart-tooltip__date">{hoverCell.day.date}</div>
          <div className="chart-tooltip__row">
            <span>Token</span>
            <span>{formatTokens(hoverCell.day.tokens)}</span>
          </div>
          <div className="chart-tooltip__row">
            <span>费用</span>
            <span>{formatCurrency(hoverCell.day.cost)}</span>
          </div>
          <div className="chart-tooltip__row">
            <span>请求</span>
            <span>{hoverCell.day.requests.toLocaleString()}</span>
          </div>
          <div className="chart-tooltip__total">
            <span>综合强度</span>
            <span>{(hoverCell.day.score * 100).toFixed(0)}%</span>
          </div>
        </div>
      )}
    </div>
  );
}
