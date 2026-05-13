import { useId } from 'react';

interface SparklineProps {
  values: number[];
  color?: string;
  filled?: boolean;
  width?: number;
  height?: number;
}

export function Sparkline({
  values,
  color = 'var(--accent)',
  filled = true,
  width = 80,
  height = 32,
}: SparklineProps) {
  const gradId = `sgrad-${useId().replace(/:/g, '')}`;
  if (!values || values.length < 2) return null;

  const min = Math.min(...values);
  const max = Math.max(...values);
  const span = max - min || 1;
  const stepX = width / (values.length - 1);
  const points = values.map<[number, number]>((v, i) => [
    i * stepX,
    height - 2 - ((v - min) / span) * (height - 4),
  ]);
  const path = 'M' + points.map(p => p.join(',')).join(' L ');
  const fillPath = path + ` L ${width},${height} L 0,${height} Z`;
  const last = points[points.length - 1];

  return (
    <svg className="kpi__spark" viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none">
      {filled && (
        <defs>
          <linearGradient id={gradId} x1="0" x2="0" y1="0" y2="1">
            <stop offset="0%" stopColor={color} stopOpacity="0.22" />
            <stop offset="100%" stopColor={color} stopOpacity="0" />
          </linearGradient>
        </defs>
      )}
      {filled && <path d={fillPath} fill={`url(#${gradId})`} />}
      <path
        d={path}
        fill="none"
        stroke={color}
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
      <circle cx={last[0]} cy={last[1]} r="2.5" fill={color} />
    </svg>
  );
}
