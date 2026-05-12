# P3 — OTLP 解析器与分发

## 项目背景

`claude-code-monitor` 是一个 Go 实现的 Claude Code 监控服务。OTLP gRPC 接收端 (P2) 已能稳定收到指标 (Metrics) 与事件 (Events)，并采集了真实样本。本阶段把 OTLP 原始消息**解析为强类型行结构体**，但仍不写库——写库放 P4。

本阶段在整体开发路径中的位置：P1 → P2 → **P3（当前） → P4 → P5**。

参考文档：
- `docs/protocol.md` — 19 个指标 / 事件的 OTLP 数据结构（字段定义、取值约束）
- `docs/models.md` — 19 张 DuckDB 表的列结构（行结构体字段与列一一对应）
- `docs/plan-p2-grpc-receiver.md` — 已有的接收端骨架与 `captured/*.pb` 测试样本来源

---

## 本阶段目标

1. 抽象公共属性提取逻辑，所有 metric / event 复用
2. 为 **8 个 metric + 11 个 event** 各写一个解析函数，输出对应表的**强类型行结构体**
3. 实现 dispatcher：按 `metric.Name` 或 `attributes["event.name"]` 路由
4. 未识别项不报错，记 `unknown` 日志（便于发现 Claude Code 新增字段）
5. 用 P2 采集的真实 `*.pb` 文件做 **golden testdata**，每种数据点至少 1 个测试用例

完成后 P2 server 改为调用 dispatcher，并把解析结果 `slog.Debug` 输出。**仍不入库**。

---

## 前置条件

- P2 已完成，`captured/metrics/*.pb` 与 `captured/logs/*.pb` 已采集
- 采集样本覆盖 19 种数据点（参见 P2 文档"开放问题"中的清单）

---

## 交付物

### 1. 行结构体定义

`internal/otlp/rows.go`（或拆成 `metric_rows.go` / `event_rows.go`）

每张表对应一个 struct，字段名与列名一一对应（驼峰 vs 下划线由 Go 命名习惯转换）。示例：

```go
type CommonAttrs struct {
    Timestamp        time.Time
    SessionID        sql.NullString
    UserID           string                  // not null
    UserAccountUUID  sql.NullString
    UserAccountID    sql.NullString
    UserEmail        sql.NullString
    OrganizationID   sql.NullString
    AppVersion       sql.NullString
    TerminalType    sql.NullString
    Attrs            map[string]any          // 未提取的剩余 attribute，写入时 marshal 为 JSON
}

type EventCommonAttrs struct {
    CommonAttrs
    EventSequence       sql.NullInt64
    PromptID            sql.NullString
    WorkspaceHostPaths  []string             // nil 表示缺省
}

type MetricSessionCountRow struct {
    CommonAttrs
    StartTimestamp time.Time
    Value          int64
    StartType      sql.NullString
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
// ... 总计 19 个 row 结构体
```

> 严格类型纪律：**不使用** `map[string]interface{}` 或 `any` 作为整体，仅 `Attrs` 兜底列用 map。

### 2. 公共属性提取

`internal/otlp/common.go`：

```go
// resourceAttrs: ResourceMetrics.Resource.Attributes 或 ResourceLogs.Resource.Attributes
// pointAttrs:    NumberDataPoint.Attributes 或 LogRecord.Attributes
func extractCommonAttrs(
    resourceAttrs []*commonpb.KeyValue,
    pointAttrs    []*commonpb.KeyValue,
    timestampNanos uint64,
) (CommonAttrs, map[string]any /* 剩余 */)
```

实现要点：
- 合并 resource + point 两层 attribute，point 层覆盖 resource 层
- 已知字段抽出，未知字段保留到剩余 map
- AnyValue 取值用一个 helper：`asString` / `asInt64` / `asDouble` / `asBool` / `asStringArray`
- `user.id` 为空时返回错误（按 protocol.md，user.id 始终存在）

事件层：

```go
func extractEventCommonAttrs(
    resourceAttrs []*commonpb.KeyValue,
    recordAttrs   []*commonpb.KeyValue,
    timestampNanos uint64,
) (EventCommonAttrs, map[string]any)
```

### 3. Metric 解析函数

`internal/otlp/metrics.go`：

每个解析函数签名：

```go
// 输入：单个 NumberDataPoint + 它所属的 Metric/Resource 上下文
// 输出：对应表的行结构体；解析失败返回 error，dispatcher 记日志后跳过
func parseSessionCount(dp *metricspb.NumberDataPoint, common CommonAttrs, leftover map[string]any) (MetricSessionCountRow, error)
func parseLinesOfCode(...)
func parsePullRequest(...)
func parseCommit(...)
func parseCostUsage(...)        // value 用 dp.AsDouble
func parseTokenUsage(...)       // value 用 dp.AsInt
func parseCodeEditDecision(...)
func parseActiveTime(...)
```

通用逻辑：
- `start_ts` 来自 `dp.StartTimeUnixNano`
- `value` 优先取 `dp.AsInt`，否则 `dp.AsDouble`；按表期望类型转换
- 类型不匹配（如 cost 收到 int）记 warn 但继续
- 各特有 attribute 从 `leftover` 中按需取出，取后从 leftover 删除，剩下的写回 `Attrs`

### 4. Event 解析函数

`internal/otlp/events.go`：

```go
func parseUserPrompt(rec *logspb.LogRecord, common EventCommonAttrs, leftover map[string]any) (EventUserPromptRow, error)
func parseApiRequest(...)
func parseApiError(...)
func parseToolResult(...)
func parseToolDecision(...)
func parseApiRetriesExhausted(...)
func parseCompaction(...)
func parsePermissionModeChanged(...)
func parseMCPServerConnection(...)
func parseSkillActivated(...)
func parseAtMention(...)
```

通用逻辑：
- `success` 等布尔字段统一接收 `"true"` / `"false"` 字符串，解析为 `bool`
- `tool_parameters` / `tool_input` 收到字符串时校验是合法 JSON，否则原样塞入 `Attrs.error`
- 缺少 `event.name` 视为错误数据点

### 5. Dispatcher

`internal/otlp/dispatch.go`：

```go
type Dispatcher struct {
    log *slog.Logger
    // 未来 P4 通过 setter 注入 sink
    sink Sink
}

type Sink interface {
    AppendMetric(any) error   // any = 具体 row struct，类型断言判断
    AppendEvent(any) error
}

type DispatchSummary struct {
    MetricRows map[string]int // metric name → 行数
    EventRows  map[string]int // event.name → 行数
    Unknown    map[string]int // 不识别 → 出现次数
    Errors     int
}

func (d *Dispatcher) DispatchMetrics(req *metricspb.ExportMetricsServiceRequest) DispatchSummary
func (d *Dispatcher) DispatchLogs(req *logspb.ExportLogsServiceRequest) DispatchSummary
```

实现要点：
- Metric 路由用 `switch metric.Name`
- Event 路由用 `switch attributes["event.name"]`
- 解析成功的行通过 `sink.AppendXxx`；P3 阶段 `sink` 是一个 NoopSink，仅计数 + Debug 日志
- 不识别的 metric / event：`summary.Unknown[name]++`，**不报错**
- 单条解析失败：`summary.Errors++` + warn 日志，继续处理同批其他点

### 6. P2 集成点改造

`internal/otlp/server.go` 接受 `*Dispatcher`，`MetricsService.Export` / `LogsService.Export` 内部替换：

```go
// 旧：summary := summarizeMetrics(req)
summary := s.dispatcher.DispatchMetrics(req)
s.log.Debug("dispatched metrics", "summary", summary)
```

Capture 行为保留不变（采集随时可关）。

### 7. 单元测试

`internal/otlp/testdata/`：从 `captured/` 中挑出有代表性的 `.pb`，重命名规范化：
```
testdata/
├── metrics/
│   ├── session_count_fresh.pb
│   ├── token_usage_input.pb
│   ├── token_usage_cache_read.pb
│   ├── cost_usage_with_skill.pb
│   ├── code_edit_decision_accept.pb
│   └── ...
└── logs/
    ├── user_prompt_command.pb
    ├── api_request_success.pb
    ├── api_error_5xx.pb
    ├── tool_result_bash_success.pb
    ├── tool_decision_user_reject.pb
    └── ...
```

测试组织：

```go
// internal/otlp/metrics_test.go
func TestParseTokenUsage(t *testing.T) {
    req := loadProto[*metricspb.ExportMetricsServiceRequest](t, "testdata/metrics/token_usage_input.pb")
    d := NewDispatcher(slog.Default(), &countingSink{})
    summary := d.DispatchMetrics(req)

    require.Equal(t, 0, summary.Errors)
    require.Equal(t, 0, len(summary.Unknown))
    require.GreaterOrEqual(t, summary.MetricRows["claude_code.token.usage"], 1)
    // 从 sink 取出 row 做字段断言
}
```

覆盖目标：每种 metric / event 至少 1 个 golden case；典型分支至少 1 个（如 `tool_result` 含 success/error 各一）。

---

## 关键技术决策

| 决策点 | 选择 | 理由 |
|---|---|---|
| Row 类型 | 19 个独立 struct | 与表 1:1，编译期就能挡掉字段写错 |
| NULL 表示 | `sql.NullXxx` | 直接给 P4 的 Appender 用，避免双重转换 |
| 兜底字段 | `Attrs map[string]any` | 仅这一处用泛型容器，对照 Java 规则的"具体类型优先" |
| Sink 接口 | P3 留好抽象，P3 自己用 noop | 避免 P4 引入循环依赖 |
| Unknown 处理 | 计数 + Debug 日志，不报错 | 暴露 Claude Code 升级新增字段而不影响主流程 |
| 单测样本 | 用 P2 采集的真实 protobuf | 比手写 protobuf golden 真实可靠 |

---

## 验收标准

```bash
# 1. 编译 + 测试
go test ./internal/otlp/...
# 期望全部通过，覆盖 19 种数据点

# 2. 服务跑起来观察 Debug 日志
LOG_LEVEL=debug ./bin/server -config config.yaml
# Claude Code 接入，期望日志可见：
# - dispatched metrics summary={claude_code.token.usage:N, ...}
# - dispatched events summary={user_prompt:N, api_request:N, ...}
# - 不应出现 unknown（如出现说明 Claude Code 新版有新指标）

# 3. 错误数据集
# 手动制造一个不识别的 metric.name，确认日志记 unknown 而非 error
```

---

## 不在本阶段范围

- 数据写入 DuckDB（P4）
- buffer / batch / flush（P4）
- 性能优化（P5 或更晚）
- 任何 HTTP 端点

---

## 留给后续阶段的接口

- `Sink` 接口由 P4 实现为真正的 `*store.BufferedWriter`
- 19 个 `XxxRow` struct 直接喂给 P4 Appender，列顺序与 DDL 对齐

---

## 开放问题

- `tool_parameters` 这种"字符串里嵌 JSON"的字段：解析时验证还是只透传？建议**只透传字符串**（DuckDB JSON 列接受任意合法 JSON 字符串，验证留给查询时）
- attribute 的 array 值（如 `workspace.host_paths`）DuckDB Appender 是否支持？需要在 P4 之前用一条样本验证 `[]string → DuckDB VARCHAR[]` 路径
- `event.timestamp` 与 `LogRecord.TimeUnixNano` 不一致时以谁为准？按 OTLP 习惯**用 `TimeUnixNano`**，`event.timestamp` 进 `Attrs` 兜底
