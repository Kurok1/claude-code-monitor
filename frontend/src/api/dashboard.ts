// Dashboard data layer.
//
// Talks to /api/usage/{snapshot,trends,rankings} on the same origin
// (Vite dev proxy points to the Go server in dev mode). Adapts the wire
// shape (snake_case, family-keyed) to a UI-friendly shape (camelCase,
// id-keyed with label/tier/color baked in).

export type ModelId = 'opus' | 'sonnet' | 'haiku' | 'other';
export type TrendFamilyId = Exclude<ModelId, 'other'>;
export type Range = 'day' | 'week' | 'month';
export type Since = '7d' | '30d' | 'all';

export interface ModelMeta {
  id: ModelId;
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

export interface SeriesPoint {
  date: string;
  label: string;
  opus: number;
  sonnet: number;
  haiku: number;
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
    hit_rate: number;
    hit_tokens: number;
    miss_tokens: number;
  };
  models: ModelBreakdown[];
  series: { points: SeriesPoint[] };
  tools: ToolUsage[];
  skills: SkillUsage[];
}

export const MODELS: ModelMeta[] = [
  { id: 'opus',   label: 'Claude Opus',   tier: '旗舰 · 复杂推理',     color: '#D97757' },
  { id: 'sonnet', label: 'Claude Sonnet', tier: '主力 · 编码 / Agent', color: '#3B6FD4' },
  { id: 'haiku',  label: 'Claude Haiku',  tier: '轻量 · 快速 / 大批量', color: '#2D7D46' },
  { id: 'other',  label: '其他模型',       tier: '未分类',              color: '#8A8580' },
];

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
    hit_rate: number;
    hit_tokens: number;
    miss_tokens: number;
  };
  models: Array<{
    family: ModelId;
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
  points: Array<{
    date: string;
    label: string;
    opus: number;
    sonnet: number;
    haiku: number;
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
  MODELS,
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
  const metaOf = (family: ModelId): ModelMeta =>
    MODELS.find(m => m.id === family) ?? MODELS[MODELS.length - 1];

  return {
    updatedAt: snap.updated_at,
    range: snap.range,
    tokens: snap.tokens,
    cost: snap.cost,
    cache: snap.cache,
    models: snap.models.map(m => ({
      ...metaOf(m.family),
      requests: m.requests,
      tokens_in: m.tokens_in,
      tokens_out: m.tokens_out,
      cache_tokens: m.cache_tokens,
      cost: m.cost,
      share: m.share,
    })),
    series: { points: trends.points },
    tools: rankings.tools,
    skills: rankings.skills,
  };
}
