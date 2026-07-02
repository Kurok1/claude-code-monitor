/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.2.0
 */

package store

import (
	"database/sql/driver"
	"fmt"
	"time"

	"github.com/kuroky/claude-code-monitor/internal/otlp"
)

// commonCodexEventCols emits the 11-column prefix shared by every codex event
// table. Column order matches internal/store/migrations/003_codex_event_tables.sql.
func commonCodexEventCols(c otlp.CodexCommonAttrs) []driver.Value {
	return []driver.Value{
		c.Timestamp,
		time.Now().UTC(),
		nullStr(c.ConversationID),
		nullStr(c.AppVersion),
		nullStr(c.AuthMode),
		nullStr(c.Originator),
		nullStr(c.TerminalType),
		nullStr(c.Model),
		nullStr(c.Slug),
		nullStr(c.UserAccountID),
		nullStr(c.UserEmail),
	}
}

func mapCodexConversationStarts(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.CodexEventConversationStartsRow)
	if !ok {
		return nil, fmt.Errorf("expected CodexEventConversationStartsRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	args := commonCodexEventCols(r.CodexCommonAttrs)
	return append(args,
		nullStr(r.ProviderName),
		nullStr(r.ReasoningEffort),
		nullStr(r.ReasoningSummary),
		nullInt64(r.ContextWindow),
		nullInt64(r.AutoCompactTokenLimit),
		nullStr(r.ApprovalPolicy),
		nullStr(r.SandboxPolicy),
		nullStr(r.MCPServers),
		attrs,
	), nil
}

func mapCodexApiRequest(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.CodexEventApiRequestRow)
	if !ok {
		return nil, fmt.Errorf("expected CodexEventApiRequestRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	args := commonCodexEventCols(r.CodexCommonAttrs)
	return append(args,
		nullInt64(r.DurationMs),
		nullInt32(r.StatusCode),
		nullStr(r.Error),
		nullInt64(r.Attempt),
		nullStr(r.Endpoint),
		attrs,
	), nil
}

func mapCodexTokenUsage(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.CodexEventTokenUsageRow)
	if !ok {
		return nil, fmt.Errorf("expected CodexEventTokenUsageRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	args := commonCodexEventCols(r.CodexCommonAttrs)
	return append(args,
		nullInt64(r.InputTokenCount),
		nullInt64(r.OutputTokenCount),
		nullInt64(r.CachedTokenCount),
		nullInt64(r.ReasoningTokenCount),
		nullInt64(r.ToolTokenCount),
		nullStr(r.ServiceTier),
		nullStr(r.ModelReasoningEffort),
		nullInt64(r.DurationMs),
		attrs,
		nullFloat64(r.CostUsd),
	), nil
}

func mapCodexUserPrompt(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.CodexEventUserPromptRow)
	if !ok {
		return nil, fmt.Errorf("expected CodexEventUserPromptRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	args := commonCodexEventCols(r.CodexCommonAttrs)
	return append(args,
		nullInt32(r.PromptLength),
		nullStr(r.Prompt),
		attrs,
	), nil
}

func mapCodexToolDecision(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.CodexEventToolDecisionRow)
	if !ok {
		return nil, fmt.Errorf("expected CodexEventToolDecisionRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	args := commonCodexEventCols(r.CodexCommonAttrs)
	return append(args,
		nullStr(r.ToolName),
		nullStr(r.CallID),
		nullStr(r.Decision),
		nullStr(r.Source),
		attrs,
	), nil
}

func mapCodexToolResult(row any) ([]driver.Value, error) {
	r, ok := row.(otlp.CodexEventToolResultRow)
	if !ok {
		return nil, fmt.Errorf("expected CodexEventToolResultRow, got %T", row)
	}
	attrs, err := attrsValue(r.Attrs)
	if err != nil {
		return nil, err
	}
	args := commonCodexEventCols(r.CodexCommonAttrs)
	return append(args,
		nullStr(r.ToolName),
		nullStr(r.CallID),
		nullInt64(r.DurationMs),
		nullBool(r.Success),
		nullStr(r.MCPServer),
		nullStr(r.MCPServerOrigin),
		nullInt64(r.ArgumentsLength),
		nullInt64(r.OutputLength),
		attrs,
	), nil
}
