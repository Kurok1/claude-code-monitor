import { useEffect, useRef, useState } from 'react';
import type { SeriesPoint } from '../../api/dashboard';
import { formatTokens } from '../../lib/format';
import { niceCeil } from './scale';

export interface ChartSeries {
  id: string;
  label: string;
  color: string;
  on: boolean;
}

interface Props {
  points: SeriesPoint[];
  series: ChartSeries[];
  height?: number;
  valueFmt?: (n: number) => string;
}

interface Stop {
  id: string;
  start: number;
  end: number;
  color: string;
}

// Safe SVG `<defs>` id from a group key — group strings may contain `.`,
// `[`, `]`, `/` which break URL-fragment refs like `url(#grad-opus-4.7)`.
function safeId(s: string): string {
  return s.replace(/[^a-zA-Z0-9_-]/g, '_');
}

export function StackedAreaChart({
  points,
  series,
  height = 280,
  valueFmt = formatTokens,
}: Props) {
  const [hover, setHover] = useState<number | null>(null);
  const wrapRef = useRef<HTMLDivElement>(null);
  const [w, setW] = useState(800);

  useEffect(() => {
    if (!wrapRef.current) return;
    const ro = new ResizeObserver(entries => {
      for (const e of entries) setW(e.contentRect.width);
    });
    ro.observe(wrapRef.current);
    return () => ro.disconnect();
  }, []);

  const padding = { top: 18, right: 16, bottom: 30, left: 56 };
  const W = w;
  const H = height;
  const innerW = W - padding.left - padding.right;
  const innerH = H - padding.top - padding.bottom;

  const active = series.filter(s => s.on);

  // Per-series areas plotted independently from the baseline (NOT stacked).
  // Real stacking made small groups visually impossible to read when peers
  // span very different scales (e.g. opus 80M vs haiku 113K — the haiku
  // line drew at opus+haiku ≈ 80M and coincided with opus).
  //
  // `total` (sum across active groups) is kept for the tooltip.
  const stack = points.map(p => {
    let sum = 0;
    const stops: Stop[] = active.map(s => {
      const v = p.values[s.id] ?? 0;
      sum += v;
      return { id: s.id, start: 0, end: v, color: s.color };
    });
    return { ...p, stops, total: sum };
  });

  const maxY = Math.max(1, ...stack.flatMap(s => s.stops.map(x => x.end)));
  const yMax = niceCeil(maxY * 1.08);

  const stepX = points.length > 1 ? innerW / (points.length - 1) : innerW;
  const xAt = (i: number) => padding.left + i * stepX;
  const yAt = (v: number) => padding.top + innerH - (v / yMax) * innerH;

  function areaPath(seriesId: string): string {
    const top = stack.map((s, i) => {
      const stop = s.stops.find(x => x.id === seriesId);
      return [xAt(i), yAt(stop ? stop.end : 0)] as [number, number];
    });
    const bottom = stack
      .map((s, i) => {
        const stop = s.stops.find(x => x.id === seriesId);
        return [xAt(i), yAt(stop ? stop.start : 0)] as [number, number];
      })
      .reverse();
    return (
      'M' +
      top.map(p => p.join(',')).join(' L ') +
      ' L ' +
      bottom.map(p => p.join(',')).join(' L ') +
      ' Z'
    );
  }

  function linePath(seriesId: string): string {
    const top = stack.map((s, i) => {
      const stop = s.stops.find(x => x.id === seriesId);
      return [xAt(i), yAt(stop ? stop.end : 0)] as [number, number];
    });
    return 'M' + top.map(p => p.join(',')).join(' L ');
  }

  const ticks = [0, 0.25, 0.5, 0.75, 1].map(t => t * yMax);
  const xLabelStep = Math.max(1, Math.ceil(points.length / 7));

  const onMove = (e: React.MouseEvent) => {
    if (!wrapRef.current) return;
    const rect = wrapRef.current.getBoundingClientRect();
    const x = ((e.clientX - rect.left) / rect.width) * W - padding.left;
    const idx = Math.round(x / stepX);
    if (idx >= 0 && idx < points.length) setHover(idx);
    else setHover(null);
  };

  return (
    <div
      className="chart-wrap"
      ref={wrapRef}
      onMouseMove={onMove}
      onMouseLeave={() => setHover(null)}
    >
      <svg className="chart-svg" viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none">
        <defs>
          {active.map(s => (
            <linearGradient
              key={s.id}
              id={`grad-${safeId(s.id)}`}
              x1="0"
              x2="0"
              y1="0"
              y2="1"
            >
              <stop offset="0%" stopColor={s.color} stopOpacity="0.55" />
              <stop offset="100%" stopColor={s.color} stopOpacity="0.05" />
            </linearGradient>
          ))}
        </defs>

        <g className="chart-grid">
          {ticks.map((t, i) => (
            <line key={i} x1={padding.left} x2={W - padding.right} y1={yAt(t)} y2={yAt(t)} />
          ))}
        </g>

        <g className="chart-axis">
          {ticks.map((t, i) => (
            <text key={i} x={padding.left - 10} y={yAt(t)} dy="0.32em" textAnchor="end">
              {valueFmt(t)}
            </text>
          ))}
        </g>

        {active.map(s => (
          <path key={s.id + '-a'} d={areaPath(s.id)} fill={`url(#grad-${safeId(s.id)})`} />
        ))}

        {active.map(s => (
          <path
            key={s.id + '-l'}
            d={linePath(s.id)}
            fill="none"
            stroke={s.color}
            strokeWidth="1.75"
            strokeLinejoin="round"
            strokeLinecap="round"
          />
        ))}

        <g className="chart-axis">
          {points.map((p, i) =>
            i % xLabelStep === 0 || i === points.length - 1 ? (
              <text key={i} x={xAt(i)} y={H - padding.bottom + 18} textAnchor="middle">
                {p.label}
              </text>
            ) : null,
          )}
        </g>

        {hover != null && (
          <g>
            <line
              x1={xAt(hover)}
              x2={xAt(hover)}
              y1={padding.top}
              y2={H - padding.bottom}
              stroke="var(--fg-3)"
              strokeWidth="1"
              strokeDasharray="3 3"
              opacity="0.5"
            />
            {stack[hover].stops.map(stop => (
              <circle
                key={stop.id}
                cx={xAt(hover)}
                cy={yAt(stop.end)}
                r="4"
                fill="var(--bg-surface)"
                stroke={stop.color}
                strokeWidth="2"
              />
            ))}
          </g>
        )}
      </svg>

      {hover != null && (
        <div
          className="chart-tooltip"
          data-visible="true"
          style={{ left: (xAt(hover) / W) * 100 + '%', top: padding.top }}
        >
          <div className="chart-tooltip__date">{points[hover].label}</div>
          {[...stack[hover].stops].reverse().map(stop => {
            const s = active.find(x => x.id === stop.id);
            return (
              <div className="chart-tooltip__row" key={stop.id}>
                <span style={{ color: stop.color }}>{s?.label}</span>
                <span>{valueFmt(stop.end - stop.start)}</span>
              </div>
            );
          })}
          <div className="chart-tooltip__total">
            <span>合计</span>
            <span>{valueFmt(stack[hover].total)}</span>
          </div>
        </div>
      )}
    </div>
  );
}
