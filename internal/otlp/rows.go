package otlp

import (
	"database/sql"
	"time"
)

// CommonAttrs is shared by all metric and event rows. Fields map 1:1 to the
// columns listed in docs/models.md §1.1 / §1.3.
type CommonAttrs struct {
	Timestamp       time.Time
	SessionID       sql.NullString
	UserID          string // required by docs/protocol.md §2.2
	UserAccountUUID sql.NullString
	UserAccountID   sql.NullString
	UserEmail       sql.NullString
	OrganizationID  sql.NullString
	AppVersion      sql.NullString
	TerminalType    sql.NullString
	Attrs           map[string]any // leftover, serialized into the attrs JSON column
}

// EventCommonAttrs adds event-only fields on top of CommonAttrs.
type EventCommonAttrs struct {
	CommonAttrs
	EventSequence      sql.NullInt64
	PromptID           sql.NullString
	WorkspaceHostPaths []string
}

// --- Metric rows (8) ---

type MetricSessionCountRow struct {
	CommonAttrs
	StartTimestamp time.Time
	Value          int64
	StartType      sql.NullString
}

type MetricLinesOfCodeCountRow struct {
	CommonAttrs
	StartTimestamp time.Time
	Value          int64
	Type           sql.NullString
}

type MetricPullRequestCountRow struct {
	CommonAttrs
	StartTimestamp time.Time
	Value          int64
}

type MetricCommitCountRow struct {
	CommonAttrs
	StartTimestamp time.Time
	Value          int64
}

type MetricCostUsageRow struct {
	CommonAttrs
	StartTimestamp  time.Time
	Value           float64
	Model           sql.NullString
	QuerySource     sql.NullString
	Speed           sql.NullString
	Effort          sql.NullString
	AgentName       sql.NullString
	SkillName       sql.NullString
	PluginName      sql.NullString
	MarketplaceName sql.NullString
}

type MetricTokenUsageRow struct {
	CommonAttrs
	StartTimestamp  time.Time
	Value           int64
	Type            sql.NullString
	Model           sql.NullString
	QuerySource     sql.NullString
	Speed           sql.NullString
	Effort          sql.NullString
	AgentName       sql.NullString
	SkillName       sql.NullString
	PluginName      sql.NullString
	MarketplaceName sql.NullString
}

type MetricCodeEditToolDecisionRow struct {
	CommonAttrs
	StartTimestamp time.Time
	Value          int64
	ToolName       sql.NullString
	Decision       sql.NullString
	Source         sql.NullString
	Language       sql.NullString
}

type MetricActiveTimeTotalRow struct {
	CommonAttrs
	StartTimestamp time.Time
	Value          float64
	Type           sql.NullString
}

// --- Event rows tier 1 (5) ---

type EventUserPromptRow struct {
	EventCommonAttrs
	PromptLength  sql.NullInt32
	Prompt        sql.NullString
	CommandName   sql.NullString
	CommandSource sql.NullString
}

type EventApiRequestRow struct {
	EventCommonAttrs
	Model               sql.NullString
	CostUsd             sql.NullFloat64
	DurationMs          sql.NullInt64
	InputTokens         sql.NullInt64
	OutputTokens        sql.NullInt64
	CacheReadTokens     sql.NullInt64
	CacheCreationTokens sql.NullInt64
	RequestID           sql.NullString
	Speed               sql.NullString
	QuerySource         sql.NullString
	Effort              sql.NullString
}

type EventApiErrorRow struct {
	EventCommonAttrs
	Model       sql.NullString
	Error       sql.NullString
	StatusCode  sql.NullInt32
	DurationMs  sql.NullInt64
	Attempt     sql.NullInt32
	RequestID   sql.NullString
	Speed       sql.NullString
	QuerySource sql.NullString
	Effort      sql.NullString
}

type EventToolResultRow struct {
	EventCommonAttrs
	ToolName            sql.NullString
	ToolUseID           sql.NullString
	Success             sql.NullBool
	DurationMs          sql.NullInt64
	ErrorType           sql.NullString
	Error               sql.NullString
	DecisionType        sql.NullString
	DecisionSource      sql.NullString
	ToolInputSizeBytes  sql.NullInt64
	ToolResultSizeBytes sql.NullInt64
	MCPServerScope      sql.NullString
	ToolParameters      sql.NullString // JSON string passthrough
	ToolInput           sql.NullString // JSON string passthrough
}

type EventToolDecisionRow struct {
	EventCommonAttrs
	ToolName  sql.NullString
	ToolUseID sql.NullString
	Decision  sql.NullString
	Source    sql.NullString
}

// --- Event rows tier 2 (6) ---

type EventApiRetriesExhaustedRow struct {
	EventCommonAttrs
	Model                sql.NullString
	Error                sql.NullString
	StatusCode           sql.NullInt32
	TotalAttempts        sql.NullInt32
	TotalRetryDurationMs sql.NullInt64
	Speed                sql.NullString
}

type EventCompactionRow struct {
	EventCommonAttrs
	Trigger    sql.NullString
	Success    sql.NullBool
	DurationMs sql.NullInt64
	PreTokens  sql.NullInt64
}

type EventPermissionModeChangedRow struct {
	EventCommonAttrs
	FromMode sql.NullString
	ToMode   sql.NullString
	Trigger  sql.NullString
}

type EventMCPServerConnectionRow struct {
	EventCommonAttrs
	Status        sql.NullString
	TransportType sql.NullString
	ServerScope   sql.NullString
	DurationMs    sql.NullInt64
	ErrorCode     sql.NullString
	ServerName    sql.NullString
	Error         sql.NullString
}

type EventSkillActivatedRow struct {
	EventCommonAttrs
	SkillName         sql.NullString
	InvocationTrigger sql.NullString
	SkillSource       sql.NullString
	PluginName        sql.NullString
	MarketplaceName   sql.NullString
}

type EventAtMentionRow struct {
	EventCommonAttrs
	MentionType sql.NullString
	Success     sql.NullBool
}
