/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.0.0
 */

// Session list + detail data layer. Talks to /api/sessions and
// /api/sessions/{id} on the same origin (Vite dev proxy → Go server).

import { getJSON } from './http';

export interface SessionSummary {
  session_id: string;
  first_active: string;
  last_active: string;
  tokens: number;
  requests: number;
  tool_calls: number;
  skill_activations: number;
}

export interface SessionListResponse {
  updated_at: string;
  sessions: SessionSummary[];
}

// Pie slices. Tools key the count as `count`, skills as `activations` —
// mirrors internal/dashboard ToolRank / SkillRank JSON tags.
export interface ToolSlice {
  name: string;
  count: number;
}
export interface SkillSlice {
  name: string;
  activations: number;
}

export interface SessionDetail {
  session_id: string;
  first_active: string;
  last_active: string;
  tokens: number;
  requests: number;
  tool_calls: number;
  skill_activations: number;
  tools: ToolSlice[];
  skills: SkillSlice[];
}

export const Sessions = {
  list(limit = 30): Promise<SessionListResponse> {
    return getJSON<SessionListResponse>(`/api/sessions?limit=${limit}`);
  },
  detail(id: string): Promise<SessionDetail> {
    return getJSON<SessionDetail>(`/api/sessions/${encodeURIComponent(id)}`);
  },
};
