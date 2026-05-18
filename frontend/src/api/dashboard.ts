// Dashboard data layer.
//
// Talks to /api/usage/{snapshot,trends,rankings} on the same origin
// (Vite dev proxy points to the Go server in dev mode). Adapts the wire
// shape (snake_case, group-keyed) to a UI-friendly shape (camelCase,
// id-keyed with label/tier/color baked in).
//
// Groups are dynamic: the backend Classifier decides them from raw model
// names + optional user-configured rules. The frontend treats group ids
// as arbitrary strings, picks colors via hash → palette, and derives a
// display label/tier from the group key (Claude family pattern → branded
// label; everything else → group name verbatim, tier "第三方模型").

export type Range = 'day' | 'week' | 'month';
export type Since = '7d' | '30d' | 'all';

export interface ModelMeta {
  id: string;
  label: string;
  tier: string;
  color: string;
}

export interface ModelBreakdown extends ModelMeta {
  requests: number;
  tokens_in: number;
  tokens_out: number;
  cache_tokens: number;
  cost: number;
  share: number;
}

// SeriesPoint maps group id → token count for one bucket of the trends chart.
// Groups missing from a bucket are treated as zero.
export interface SeriesPoint {
  date: string;
  label: string;
  values: Record<string, number>;
}

export interface ToolUsage {
  name: string;
  count: number;
}

export interface SkillUsage {
  name: string;
  activations: number;
}

export interface DashboardData {
  updatedAt: string;
  range: Range;
  tokens: {
    in: number;
    out: number;
    total: number;
    prev_total: number;
    sparkline: number[];
  };
  cost: {
    total: number;
    prev_total: number;
    sparkline: number[];
  };
  cache: {
    hit_rate: number | null;
    read_tokens: number;
    creation_tokens: number;
  };
  models: ModelBreakdown[];
  series: {
    groups: ModelMeta[]; // legend order from API + UI metadata
    points: SeriesPoint[];
  };
  tools: ToolUsage[];
  skills: SkillUsage[];
}

// ─────────────────────────────────────────────────────────────────────
// Group → display metadata
// ─────────────────────────────────────────────────────────────────────

const PALETTE = [
  '#D97757', // Claude clay (opus signature)
  '#3B6FD4', // sonnet blue
  '#2D7D46', // haiku green
  '#8B5A2B', // amber
  '#7B4E9A', // violet
  '#D4860A', // gold
  '#1E7A99', // teal
  '#A8502C', // rust
  '#274EA0', // indigo
  '#1E5730', // forest
];

const CLAUDE_TIER: Record<string, string> = {
  opus: '旗舰 · 复杂推理',
  sonnet: '主力 · 编码 / Agent',
  haiku: '轻量 · 快速 / 大批量',
};

const CLAUDE_GROUP_RE = /^(opus|sonnet|haiku)-(\d+\.\d+)$/;

// Stable hash for deterministic color assignment. Same group string always
// produces the same palette slot across reloads.
function hashString(s: string): number {
  let h = 2166136261;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return h >>> 0;
}

function colorFor(group: string): string {
  return PALETTE[hashString(group) % PALETTE.length];
}

function capitalize(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

export function metaForGroup(group: string): ModelMeta {
  const m = group.match(CLAUDE_GROUP_RE);
  if (m) {
    const family = m[1];
    const ver = m[2];
    return {
      id: group,
      label: `Claude ${capitalize(family)} ${ver}`,
      tier: CLAUDE_TIER[family] ?? '',
      color: colorFor(group),
    };
  }
  return {
    id: group,
    label: group,
    tier: '第三方模型',
    color: colorFor(group),
  };
}

// ─────────────────────────────────────────────────────────────────────
// Wire types (snake_case, mirrors internal/dashboard/types.go)
// ─────────────────────────────────────────────────────────────────────

interface SnapshotWire {
  updated_at: string;
  range: Range;
  tokens: {
    in: number;
    out: number;
    total: number;
    prev_total: number;
    sparkline: number[];
  };
  cost: {
    total: number;
    prev_total: number;
    sparkline: number[];
  };
  cache: {
    hit_rate: number | null;
    read_tokens: number;
    creation_tokens: number;
  };
  models: Array<{
    group: string;
    requests: number;
    tokens_in: number;
    tokens_out: number;
    cache_tokens: number;
    cost: number;
    share: number;
  }>;
}

interface TrendsWire {
  range: Range;
  groups: string[];
  points: Array<{
    date: string;
    label: string;
    values: Record<string, number>;
  }>;
}

interface RankingsWire {
  since: Since;
  tools: ToolUsage[];
  skills: SkillUsage[];
}

// ─────────────────────────────────────────────────────────────────────
// Public API
// ─────────────────────────────────────────────────────────────────────

async function getJSON<T>(url: string): Promise<T> {
  const r = await fetch(url, { credentials: 'same-origin' });
  if (!r.ok) {
    const body = await r.text().catch(() => '');
    throw new Error(`GET ${url} → ${r.status}: ${body}`);
  }
  return (await r.json()) as T;
}

export const Dashboard = {
  async fetch(range: Range = 'day', since: Since = '7d'): Promise<DashboardData> {
    const [snap, trends, rankings] = await Promise.all([
      getJSON<SnapshotWire>(`/api/usage/snapshot?range=${range}`),
      getJSON<TrendsWire>(`/api/usage/trends?range=${range}`),
      getJSON<RankingsWire>(`/api/usage/rankings?since=${since}`),
    ]);
    return adapt(snap, trends, rankings);
  },
};

function adapt(snap: SnapshotWire, trends: TrendsWire, rankings: RankingsWire): DashboardData {
  return {
    updatedAt: snap.updated_at,
    range: snap.range,
    tokens: snap.tokens,
    cost: snap.cost,
    cache: snap.cache,
    models: snap.models.map(m => ({
      ...metaForGroup(m.group),
      requests: m.requests,
      tokens_in: m.tokens_in,
      tokens_out: m.tokens_out,
      cache_tokens: m.cache_tokens,
      cost: m.cost,
      share: m.share,
    })),
    series: {
      groups: trends.groups.map(g => metaForGroup(g)),
      points: trends.points.map(p => ({
        date: p.date,
        label: p.label,
        values: p.values ?? {},
      })),
    },
    tools: rankings.tools,
    skills: rankings.skills,
  };
}
