package otlp

import (
	"fmt"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	lpb "go.opentelemetry.io/proto/otlp/logs/v1"
)

// preparseEvent prepares attribute map + timestamp + common attrs, common to
// all 11 event parsers. event.name is removed by the caller (dispatcher) so
// parsers never see it in leftover.
func preparseEvent(rec *lpb.LogRecord, resourceAttrs []*commonpb.KeyValue) (attrMap, EventCommonAttrs, error) {
	m := newAttrMap(resourceAttrs, rec.Attributes)
	delete(m, "event.name")
	ts := time.Unix(0, int64(rec.TimeUnixNano)).UTC()
	common, err := extractEventCommonAttrs(m, ts)
	return m, common, err
}

// --- Tier 1 ---

func parseUserPrompt(rec *lpb.LogRecord, resourceAttrs []*commonpb.KeyValue) (EventUserPromptRow, error) {
	m, common, err := preparseEvent(rec, resourceAttrs)
	if err != nil {
		return EventUserPromptRow{}, fmt.Errorf("user_prompt: %w", err)
	}
	row := EventUserPromptRow{
		EventCommonAttrs: common,
		PromptLength:     m.takeInt32("prompt_length"),
		Prompt:           m.takeString("prompt"),
		CommandName:      m.takeString("command_name"),
		CommandSource:    m.takeString("command_source"),
	}
	row.Attrs = m.leftover()
	return row, nil
}

func parseApiRequest(rec *lpb.LogRecord, resourceAttrs []*commonpb.KeyValue) (EventApiRequestRow, error) {
	m, common, err := preparseEvent(rec, resourceAttrs)
	if err != nil {
		return EventApiRequestRow{}, fmt.Errorf("api_request: %w", err)
	}
	row := EventApiRequestRow{
		EventCommonAttrs:    common,
		Model:               m.takeString("model"),
		CostUsd:             m.takeFloat64("cost_usd"),
		DurationMs:          m.takeInt64("duration_ms"),
		InputTokens:         m.takeInt64("input_tokens"),
		OutputTokens:        m.takeInt64("output_tokens"),
		CacheReadTokens:     m.takeInt64("cache_read_tokens"),
		CacheCreationTokens: m.takeInt64("cache_creation_tokens"),
		RequestID:           m.takeString("request_id"),
		Speed:               m.takeString("speed"),
		QuerySource:         m.takeString("query_source"),
		Effort:              m.takeString("effort"),
	}
	row.Attrs = m.leftover()
	return row, nil
}

func parseApiError(rec *lpb.LogRecord, resourceAttrs []*commonpb.KeyValue) (EventApiErrorRow, error) {
	m, common, err := preparseEvent(rec, resourceAttrs)
	if err != nil {
		return EventApiErrorRow{}, fmt.Errorf("api_error: %w", err)
	}
	row := EventApiErrorRow{
		EventCommonAttrs: common,
		Model:            m.takeString("model"),
		Error:            m.takeString("error"),
		StatusCode:       m.takeInt32("status_code"),
		DurationMs:       m.takeInt64("duration_ms"),
		Attempt:          m.takeInt32("attempt"),
		RequestID:        m.takeString("request_id"),
		Speed:            m.takeString("speed"),
		QuerySource:      m.takeString("query_source"),
		Effort:           m.takeString("effort"),
	}
	row.Attrs = m.leftover()
	return row, nil
}

func parseToolResult(rec *lpb.LogRecord, resourceAttrs []*commonpb.KeyValue) (EventToolResultRow, error) {
	m, common, err := preparseEvent(rec, resourceAttrs)
	if err != nil {
		return EventToolResultRow{}, fmt.Errorf("tool_result: %w", err)
	}
	row := EventToolResultRow{
		EventCommonAttrs:    common,
		ToolName:            m.takeString("tool_name"),
		ToolUseID:           m.takeString("tool_use_id"),
		Success:             m.takeBool("success"),
		DurationMs:          m.takeInt64("duration_ms"),
		ErrorType:           m.takeString("error_type"),
		Error:               m.takeString("error"),
		DecisionType:        m.takeString("decision_type"),
		DecisionSource:      m.takeString("decision_source"),
		ToolInputSizeBytes:  m.takeInt64("tool_input_size_bytes"),
		ToolResultSizeBytes: m.takeInt64("tool_result_size_bytes"),
		MCPServerScope:      m.takeString("mcp_server_scope"),
		ToolParameters:      m.takeString("tool_parameters"),
		ToolInput:           m.takeString("tool_input"),
	}
	row.Attrs = m.leftover()
	return row, nil
}

func parseToolDecision(rec *lpb.LogRecord, resourceAttrs []*commonpb.KeyValue) (EventToolDecisionRow, error) {
	m, common, err := preparseEvent(rec, resourceAttrs)
	if err != nil {
		return EventToolDecisionRow{}, fmt.Errorf("tool_decision: %w", err)
	}
	row := EventToolDecisionRow{
		EventCommonAttrs: common,
		ToolName:         m.takeString("tool_name"),
		ToolUseID:        m.takeString("tool_use_id"),
		Decision:         m.takeString("decision"),
		Source:           m.takeString("source"),
	}
	row.Attrs = m.leftover()
	return row, nil
}

// --- Tier 2 ---

func parseApiRetriesExhausted(rec *lpb.LogRecord, resourceAttrs []*commonpb.KeyValue) (EventApiRetriesExhaustedRow, error) {
	m, common, err := preparseEvent(rec, resourceAttrs)
	if err != nil {
		return EventApiRetriesExhaustedRow{}, fmt.Errorf("api_retries_exhausted: %w", err)
	}
	row := EventApiRetriesExhaustedRow{
		EventCommonAttrs:     common,
		Model:                m.takeString("model"),
		Error:                m.takeString("error"),
		StatusCode:           m.takeInt32("status_code"),
		TotalAttempts:        m.takeInt32("total_attempts"),
		TotalRetryDurationMs: m.takeInt64("total_retry_duration_ms"),
		Speed:                m.takeString("speed"),
	}
	row.Attrs = m.leftover()
	return row, nil
}

func parseCompaction(rec *lpb.LogRecord, resourceAttrs []*commonpb.KeyValue) (EventCompactionRow, error) {
	m, common, err := preparseEvent(rec, resourceAttrs)
	if err != nil {
		return EventCompactionRow{}, fmt.Errorf("compaction: %w", err)
	}
	row := EventCompactionRow{
		EventCommonAttrs: common,
		Trigger:          m.takeString("trigger"),
		Success:          m.takeBool("success"),
		DurationMs:       m.takeInt64("duration_ms"),
		PreTokens:        m.takeInt64("pre_tokens"),
	}
	row.Attrs = m.leftover()
	return row, nil
}

func parsePermissionModeChanged(rec *lpb.LogRecord, resourceAttrs []*commonpb.KeyValue) (EventPermissionModeChangedRow, error) {
	m, common, err := preparseEvent(rec, resourceAttrs)
	if err != nil {
		return EventPermissionModeChangedRow{}, fmt.Errorf("permission_mode_changed: %w", err)
	}
	row := EventPermissionModeChangedRow{
		EventCommonAttrs: common,
		FromMode:         m.takeString("from_mode"),
		ToMode:           m.takeString("to_mode"),
		Trigger:          m.takeString("trigger"),
	}
	row.Attrs = m.leftover()
	return row, nil
}

func parseMCPServerConnection(rec *lpb.LogRecord, resourceAttrs []*commonpb.KeyValue) (EventMCPServerConnectionRow, error) {
	m, common, err := preparseEvent(rec, resourceAttrs)
	if err != nil {
		return EventMCPServerConnectionRow{}, fmt.Errorf("mcp_server_connection: %w", err)
	}
	row := EventMCPServerConnectionRow{
		EventCommonAttrs: common,
		Status:           m.takeString("status"),
		TransportType:    m.takeString("transport_type"),
		ServerScope:      m.takeString("server_scope"),
		DurationMs:       m.takeInt64("duration_ms"),
		ErrorCode:        m.takeString("error_code"),
		ServerName:       m.takeString("server_name"),
		Error:            m.takeString("error"),
	}
	row.Attrs = m.leftover()
	return row, nil
}

func parseSkillActivated(rec *lpb.LogRecord, resourceAttrs []*commonpb.KeyValue) (EventSkillActivatedRow, error) {
	m, common, err := preparseEvent(rec, resourceAttrs)
	if err != nil {
		return EventSkillActivatedRow{}, fmt.Errorf("skill_activated: %w", err)
	}
	row := EventSkillActivatedRow{
		EventCommonAttrs:  common,
		SkillName:         m.takeString("skill.name"),
		InvocationTrigger: m.takeString("invocation_trigger"),
		SkillSource:       m.takeString("skill.source"),
		PluginName:        m.takeString("plugin.name"),
		MarketplaceName:   m.takeString("marketplace.name"),
	}
	row.Attrs = m.leftover()
	return row, nil
}

func parseAtMention(rec *lpb.LogRecord, resourceAttrs []*commonpb.KeyValue) (EventAtMentionRow, error) {
	m, common, err := preparseEvent(rec, resourceAttrs)
	if err != nil {
		return EventAtMentionRow{}, fmt.Errorf("at_mention: %w", err)
	}
	row := EventAtMentionRow{
		EventCommonAttrs: common,
		MentionType:      m.takeString("mention_type"),
		Success:          m.takeBool("success"),
	}
	row.Attrs = m.leftover()
	return row, nil
}
