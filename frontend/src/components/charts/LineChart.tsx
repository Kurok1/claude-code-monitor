/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.5.0
 */

import { useEffect, useRef, useState } from 'react';
import type { SeriesPoint } from '../../api/dashboard';
import type { ChartSeries } from './StackedAreaChart';
import { niceCeil } from './StackedAreaChart';

interface Props {
  points: SeriesPoint[];
  series: ChartSeries[];
  height?: number;
  valueFmt?: (n: number) => string;
}

// 多系列折线图:values 中缺失的 key 视为"该桶无数据",线在此断开。
// 与 StackedAreaChart 共用 chart-wrap/chart-svg/chart-grid/chart-axis/
// chart-tooltip 的样式类。
export function LineChart({
  points,
  series,
  height = 280,
  valueFmt = n => n.toFixed(1),
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

  const allValues = points.flatMap(p =>
    active.map(s => p.values[s.id]).filter((v): v is number => v != null),
  );
  const maxY = Math.max(1, ...allValues);
  const yMax = niceCeil(maxY * 1.08);

  const stepX = points.length > 1 ? innerW / (points.length - 1) : innerW;
  const xAt = (i: number) => padding.left + i * stepX;
  const yAt = (v: number) => padding.top + innerH - (v / yMax) * innerH;

  // 每个系列切成连续段:缺失值处断线;孤立单点画圆。
  function segmentsOf(seriesId: string): Array<Array<[number, number]>> {
    const segs: Array<Array<[number, number]>> = [];
    let cur: Array<[number, number]> = [];
    points.forEach((p, i) => {
      const v = p.values[seriesId];
      if (v == null) {
        if (cur.length) segs.push(cur);
        cur = [];
        return;
      }
      cur.push([xAt(i), yAt(v)]);
    });
    if (cur.length) segs.push(cur);
    return segs;
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

  const hoverRows =
    hover == null
      ? []
      : active
          .map(s => ({ s, v: points[hover].values[s.id] }))
          .filter((r): r is { s: ChartSeries; v: number } => r.v != null);

  return (
    <div
      className="chart-wrap"
      ref={wrapRef}
      onMouseMove={onMove}
      onMouseLeave={() => setHover(null)}
    >
      <svg className="chart-svg" viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none">
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

        {active.map(s =>
          segmentsOf(s.id).map((seg, gi) =>
            seg.length === 1 ? (
              <circle key={`${s.id}-p${gi}`} cx={seg[0][0]} cy={seg[0][1]} r="2.5" fill={s.color} />
            ) : (
              <path
                key={`${s.id}-l${gi}`}
                d={'M' + seg.map(p => p.join(',')).join(' L ')}
                fill="none"
                stroke={s.color}
                strokeWidth="1.75"
                strokeLinejoin="round"
                strokeLinecap="round"
              />
            ),
          ),
        )}

        <g className="chart-axis">
          {points.map((p, i) =>
            i % xLabelStep === 0 || i === points.length - 1 ? (
              <text key={i} x={xAt(i)} y={H - padding.bottom + 18} textAnchor="middle">
                {p.label}
              </text>
            ) : null,
          )}
        </g>

        {hover != null && hoverRows.length > 0 && (
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
            {hoverRows.map(({ s, v }) => (
              <circle
                key={s.id}
                cx={xAt(hover)}
                cy={yAt(v)}
                r="4"
                fill="var(--bg-surface)"
                stroke={s.color}
                strokeWidth="2"
              />
            ))}
          </g>
        )}
      </svg>

      {hover != null && hoverRows.length > 0 && (
        <div
          className="chart-tooltip"
          data-visible="true"
          style={{ left: (xAt(hover) / W) * 100 + '%', top: padding.top }}
        >
          <div className="chart-tooltip__date">{points[hover].label}</div>
          {hoverRows.map(({ s, v }) => (
            <div className="chart-tooltip__row" key={s.id}>
              <span style={{ color: s.color }}>{s.label}</span>
              <span>{valueFmt(v)}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
