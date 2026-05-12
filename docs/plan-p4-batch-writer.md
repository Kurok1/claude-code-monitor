# P4 — 批量写入（Appender + buffer + 优雅关闭）

## 项目背景

`claude-code-monitor` 是一个 Go 实现的 Claude Code 监控服务。gRPC 接收端 (P2) 与 OTLP 解析器 (P3) 已就绪，现在把解析后的强类型行结构体真正落到 DuckDB。

本阶段在整体开发路径中的位置：P1 → P2 → P3 → **P4（当前） → P5**。

参考文档：
- `docs/models.md` — 19 张表的 DDL（列顺序在 Appender 中严格对齐）
- `docs/plan-p3-otlp-parser.md` — 提供 19 个 `XxxRow` struct 与 `Sink` 接口

---

## 本阶段目标

实现从 dispatcher 输出到 DuckDB 落库的完整 ingest 管线：

1. 每张表独立的内存 buffer + 触发条件（行数或时间）
2. 用 DuckDB **Appender API** 批量写入（go-duckdb 提供，比 `INSERT VALUES` 快一个数量级）
3. 背压策略：buffer 达到硬上限时丢最旧并记日志，**不反压 gRPC**
4. 优雅关闭：SIGTERM → 停 gRPC → flush 全部 buffer → 关 DB

完成后 Claude Code 接入运行 10 分钟，19 张表都有正确数据可查。

---

## 前置条件

- P3 已完成：dispatcher 输出 19 种 `XxxRow`，已用真实 testdata 测试
- P3 留下的 `Sink` 接口骨架
- P1 的 `*store.DB` 可拿到底层 driver 连接以使用 Appender

---

## 交付物

### 1. Appender 适配层

`internal/store/appender.go`：

go-duckdb 的 Appender API 用法（伪代码）：

```go
import "github.com/marcboeker/go-duckdb"

connector, err := duckdb.NewConnector(path, nil)
conn, err := connector.Connect(ctx)
appender, err := duckdb.NewAppenderFromConn(conn, "" /* schema */, "metric_token_usage")
defer appender.Close()

// 每行
appender.AppendRow(args...) // 按 DDL 顺序传值
appender.Flush()
```

封装为类型安全的 per-table appender：

```go
type Appender interface {
    Append(row any) error  // 内部按 row 实际类型分发，类型不匹配返回 error
    Flush() error
    Close() error
}

// 每张表一个具体实现：
type metricTokenUsageAppender struct {
    inner *duckdb.Appender
}
func (a *metricTokenUsageAppender) Append(row any) error {
    r, ok := row.(MetricTokenUsageRow)
    if !ok { return fmt.Errorf("unexpected row type %T", row) }
    return a.inner.AppendRow(
        r.Timestamp, r.StartTimestamp, time.Now(), r.Value,
        nullStr(r.SessionID), r.UserID, nullStr(r.UserAccountUUID), /* ... 按 DDL 顺序 ... */
        attrsJSON(r.Attrs),
    )
}
```

设计要点：
- **列顺序必须与 DDL 完全一致**，每个 appender 自带一份顺序定义（写测试断言）
- `sql.NullXxx` → 原始指针或 nil（看 go-duckdb 版本支持）
- `time.Time` 直接传，go-duckdb 转 TIMESTAMP
- `map[string]any` (attrs) → `json.Marshal` 后传 string
- `[]string` (workspace_host_paths) → 直接传切片，go-duckdb 转 VARCHAR[]（**P3 已留 TODO，需在此验证**）

### 2. TableBuffer

`internal/store/buffer.go`：

```go
type TableBuffer struct {
    name      string
    mu        sync.Mutex
    rows      []any
    appender  Appender

    batchSize     int
    flushInterval time.Duration
    hardLimit     int

    lastFlush time.Time
    metrics   *BufferMetrics // 简单计数：appended / flushed / dropped
}

func NewTableBuffer(name string, app Appender, cfg config.IngestConfig) *TableBuffer

func (b *TableBuffer) Append(row any) {
    b.mu.Lock()
    defer b.mu.Unlock()

    if len(b.rows) >= b.hardLimit {
        // 丢最旧
        drop := len(b.rows) - b.hardLimit + 1
        b.rows = b.rows[drop:]
        b.metrics.dropped += int64(drop)
        slog.Warn("buffer hard limit, dropped oldest", "table", b.name, "dropped", drop)
    }
    b.rows = append(b.rows, row)

    if len(b.rows) >= b.batchSize {
        b.flushLocked() // 同步 flush，吞下错误（已记日志）
    }
}

func (b *TableBuffer) flushLocked() { /* appender.Append for each; appender.Flush; reset */ }

// 周期 flush 由外部 ticker 调用
func (b *TableBuffer) MaybeFlushByTime() { /* lock, check lastFlush > interval, flushLocked */ }

func (b *TableBuffer) FlushAndClose() error { /* 最终 flush + appender.Close */ }
```

### 3. BufferedWriter / Sink 实现

`internal/store/writer.go`：

```go
type BufferedWriter struct {
    buffers map[string]*TableBuffer  // 表名 → buffer
    ticker  *time.Ticker
    stopCh  chan struct{}
}

func NewBufferedWriter(db *DB, cfg config.IngestConfig) (*BufferedWriter, error) {
    // 为 19 张表各创建 TableBuffer + Appender
}

// 实现 P3 的 Sink 接口
func (w *BufferedWriter) AppendMetric(row any) error {
    table := tableNameFor(row) // 类型断言映射到表名
    return w.buffers[table].Append(row), nil
}
func (w *BufferedWriter) AppendEvent(row any) error { /* 同上 */ }

func (w *BufferedWriter) Start() { /* 启动 ticker，按 flush_interval 触发 MaybeFlushByTime */ }
func (w *BufferedWriter) Stop() error { /* 停 ticker → 全表 FlushAndClose */ }
```

`tableNameFor` 实现：

```go
func tableNameFor(row any) string {
    switch row.(type) {
    case MetricSessionCountRow:        return "metric_session_count"
    case MetricLinesOfCodeCountRow:    return "metric_lines_of_code_count"
    // ... 19 个 case
    }
    panic("unknown row type")
}
```

### 4. 串联到 P2/P3

`cmd/server/main.go`：

```go
writer, _ := store.NewBufferedWriter(db, cfg.Ingest)
writer.Start()
defer writer.Stop()

dispatcher := otlp.NewDispatcher(slog.Default(), writer)  // P3 的 Sink 注入真实实现
srv := otlp.NewServer(cfg, dispatcher)
go srv.Start()
waitForSignal()
srv.GracefulStop()       // 1. 停 gRPC
writer.Stop()            // 2. flush + close
db.Close()               // 3. 关 DB
```

### 5. DuckDB 单写者并发约束

DuckDB 不支持多连接并发写。两种方案：

**方案 A（推荐）**：每个 Appender 复用一个独立的 driver 连接，**所有 flush 操作串行化**到一个 goroutine。
- 19 个 buffer 的 `flushLocked` 实际是把数据投递到一个 flush 通道
- 一个专门的 flusher goroutine 顺序消费通道，调用对应 Appender

**方案 B**：每张表自己持有 connection，全局 mutex 保证一次只有一个 Appender 活跃。
- 实现简单，但任何写阻塞所有写

**先采用方案 B**：实现简单，监控数据吞吐远低于 DuckDB 极限。若 P5 压测发现瓶颈再切方案 A。

### 6. 异常路径

| 场景 | 处理 |
|---|---|
| 单行 AppendRow 失败 | warn 日志 + 跳过本行；不影响 batch 其他行（Appender API 行级独立） |
| `appender.Flush()` 失败 | error 日志 + buffer 数据**保留**等下次 flush 重试；连续 N 次失败上抛进程级告警 |
| DuckDB 文件锁 / 磁盘满 | flush 失败 → 进入 degraded 模式：log error 后继续 buffer，buffer 满则按背压丢弃 |
| 优雅关闭超时（>30s） | 强制 close，记录未 flush 行数 |

### 7. 自监控（最小）

`BufferMetrics`：

```go
type BufferMetrics struct {
    Appended uint64
    Flushed  uint64
    Dropped  uint64
    LastFlushMs int64
    FlushErrors uint64
}
```

P5 会把这些暴露成 `/internal/stats`。本阶段先在内存中累加，并在 flush 完成时 `slog.Debug` 输出。

---

## 关键技术决策

| 决策点 | 选择 | 理由 |
|---|---|---|
| 写入 API | DuckDB Appender | 比 `INSERT VALUES` 快 10x+，是写入大批量数据的官方推荐路径 |
| 并发模型 | 全局 mutex 串行 flush | 监控吞吐远低于 DuckDB 极限，简单优先 |
| 背压策略 | 丢最旧 + 日志 | OTLP 客户端 SDK 满了本身就会丢，监控不能反压 |
| Flush 失败重试 | 保留 buffer 等下一轮 | 临时性磁盘 / 锁问题；连续失败提升告警级别 |
| Appender Close 时机 | 每次 batch flush 不 close，只在 Stop 时 close | Appender 生命周期跟随 connection，频繁开关浪费 |
| Time 列时区 | UTC，存储为 TIMESTAMP（无时区） | DuckDB TIMESTAMP 无时区；查询时按需 `AT TIME ZONE 'Asia/Shanghai'` |

---

## 验收标准

```bash
# 1. 编译 + 单测
go test ./internal/store/...

# 2. 端到端冒烟
./bin/server -config config.yaml
# Claude Code 接入正常使用 10 分钟

# 3. 数据验证
duckdb ./data/monitor.duckdb <<SQL
SELECT 'metric_token_usage' AS t, COUNT(*) FROM metric_token_usage
UNION ALL SELECT 'metric_cost_usage', COUNT(*) FROM metric_cost_usage
UNION ALL SELECT 'event_user_prompt', COUNT(*) FROM event_user_prompt
UNION ALL SELECT 'event_api_request', COUNT(*) FROM event_api_request
UNION ALL SELECT 'event_tool_result', COUNT(*) FROM event_tool_result;
SQL
# 期望：所有表均 > 0；按预期使用模式数据量大致符合

# 4. 字段抽样
duckdb ./data/monitor.duckdb "SELECT ts, model, type, value FROM metric_token_usage ORDER BY ts DESC LIMIT 10;"
# 期望：ts 单调降序，model 是真实模型 ID，type 在 input/output/cacheRead/cacheCreation，value > 0

# 5. 优雅关闭无丢失
# 写入期间 kill -TERM <pid>，重启后查询计数
# 期望：关闭前的最后一个 batch 已落库，无 partial row

# 6. 背压
# 用 grpcurl 灌大量 metric（10x 速率），观察日志
# 期望：buffer hard limit 触发，dropped 计数增长，进程不 OOM
```

---

## 不在本阶段范围

- HTTP / 查询 API（v2）
- 数据归档到 parquet（v2 或更晚）
- 自监控的 HTTP 暴露端点（P5）
- 压力测试与性能调优（P5）

---

## 留给后续阶段的接口

- `BufferMetrics` 内的计数给 P5 的 `/internal/stats` 端点直接读
- `*BufferedWriter` 暴露 `Stats()` 方法返回快照

---

## 开放问题

- `VARCHAR[]` 列（`workspace_host_paths`）Appender 写入路径需验证。如果 go-duckdb 当前版本不支持原生数组 append，回退方案：在 P3 解析时把数组序列化为 JSON 字符串塞入 `attrs`，DDL 改 `workspace_host_paths VARCHAR[]` 为 `workspace_host_paths VARCHAR`（JSON）。在 P4 实施前一周内做最小验证脚本拍板。
- batch_size 默认 500 是否合适：可在 P5 压测时调整
- DuckDB checkpoint 频率：默认 WAL 自动 checkpoint，本阶段不干预；如发现 `.duckdb` 文件不收敛再加 `PRAGMA force_checkpoint`
