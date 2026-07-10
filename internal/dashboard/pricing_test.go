/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.5.0
 */

package dashboard

import (
	"context"
	"testing"
	"time"
)

func TestQuerySeenModels(t *testing.T) {
	db, _, _ := testDB(t)
	at := time.Date(2026, 5, 13, 1, 0, 0, 0, time.UTC)
	later := at.Add(time.Hour)

	insertApiRequest(t, db, at, "claude-opus-4-8")
	insertApiRequest(t, db, later, "claude-opus-4-8")
	// metric 臂只补覆盖:requests 记 0,不与 api_request 重复计数
	insertTokenUsage(t, db, later.Add(time.Hour), "claude-opus-4-8", "input", 100)
	// 过滤:合成占位模型
	insertApiRequest(t, db, at, "<synthetic>")
	// codex 臂
	insertRateCodexUsage(t, db, at, "gpt-5.1-codex", 100, 50, 0, 1000)

	rows, err := QuerySeenModels(context.Background(), db, ClientAll)
	if err != nil {
		t.Fatalf("QuerySeenModels: %v", err)
	}
	type merged struct {
		requests int64
		lastSeen time.Time
		clients  map[string]bool
	}
	byModel := map[string]*merged{}
	for _, r := range rows {
		m := byModel[r.Model]
		if m == nil {
			m = &merged{clients: map[string]bool{}}
			byModel[r.Model] = m
		}
		m.requests += r.Requests
		if r.LastSeen.After(m.lastSeen) {
			m.lastSeen = r.LastSeen
		}
		m.clients[r.Client] = true
	}
	if len(byModel) != 2 {
		t.Fatalf("models = %v, want 2 (opus + codex, synthetic filtered)", byModel)
	}
	opus := byModel["claude-opus-4-8"]
	if opus == nil || opus.requests != 2 || !opus.clients["claude"] {
		t.Errorf("opus = %+v, want requests=2 client=claude", opus)
	}
	// last_seen 取 metric 臂更晚的时间
	if !opus.lastSeen.Equal(later.Add(time.Hour)) {
		t.Errorf("opus lastSeen = %v, want %v", opus.lastSeen, later.Add(time.Hour))
	}
	codex := byModel["gpt-5.1-codex"]
	if codex == nil || codex.requests != 1 || !codex.clients["codex"] {
		t.Errorf("codex = %+v, want requests=1 client=codex", codex)
	}

	// 单臂过滤
	claudeOnly, err := QuerySeenModels(context.Background(), db, ClientClaude)
	if err != nil {
		t.Fatalf("QuerySeenModels(claude): %v", err)
	}
	for _, r := range claudeOnly {
		if r.Client != "claude" {
			t.Errorf("claude arm returned %s row", r.Client)
		}
	}
}
