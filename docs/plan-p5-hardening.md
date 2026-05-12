# P5 — 运维收尾（日志 / 自监控 / 冒烟测试）

## 项目背景

`claude-code-monitor` 是一个 Go 实现的 Claude Code 监控服务。P1–P4 已完成从 OTLP gRPC 接收到 DuckDB 落库的完整管线。本阶段做发布前的工程化收尾：可观测性、文档、自动化冒烟。

本阶段在整体开发路径中的位置：P1 → P2 → P3 → P4 → **P5（当前）**。
完成后即可发布 v1（后端版本），v2 接续做 HTTP 查询 API + 前端。

参考文档：
- `docs/plan-p4-batch-writer.md` — `BufferedWriter.Stats()` 已留接口
- `docs/protocol.md` / `docs/models.md` — 字段与表结构

---

## 本阶段目标

1. 日志系统标准化（结构化 + 可切格式 + 可调级别）
2. 自监控指标通过 HTTP 端点暴露，方便排查
3. README 写到能让陌生人 30 分钟内跑起来
4. 端到端冒烟测试脚本化，避免每次手测
5. 24h 稳定性验证（无 panic、内存稳定）

---

## 前置条件

- P4 已完成：完整 ingest 管线可用
- `BufferedWriter.Stats()` 返回 `map[string]BufferMetrics`

---

## 交付物

### 1. 日志系统标准化

`internal/logging/setup.go`：

```go
func Setup(cfg config.LoggingConfig) *slog.Logger {
    level := parseLevel(cfg.Level)  // debug/info/warn/error
    var h slog.Handler
    switch cfg.Format {
    case "json":
        h = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
    case "text":
        h = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})
    default:
        return nil // 启动时验证已拒绝
    }
    logger := slog.New(h)
    slog.SetDefault(logger)
    return logger
}
```

约定：
- 日志键名一律 `snake_case`
- 错误用 `slog.Error("...", "err", err)`，不要嵌入 message 字符串
- 高频路径（每个 Export 一条）走 `Info`；逐行操作走 `Debug`
- 启动 / 关闭 / 迁移 / 致命错误用 `Info` / `Error`

### 2. 自监控端点

`internal/stats/server.go`：

```go
type Stats struct {
    StartTime  time.Time
    BufferStats map[string]store.BufferMetrics  // 19 张表
    GRPC struct {
        MetricsReqs uint64
        LogsReqs    uint64
        BytesIn     uint64
    }
}

// 监听一个独立 HTTP 端口（配置项 stats_listen），如 :9100
// GET /internal/stats   → 纯文本表格（不引入 prometheus 依赖）
// GET /internal/healthz → "ok"
// GET /debug/pprof/...  → 标准库 net/http/pprof
```

输出示例：

```
# claude-code-monitor stats
uptime_seconds        12345
grpc_metrics_reqs     8920
grpc_logs_reqs        17834
grpc_bytes_in         45612345

# per-table buffers
table                          appended  flushed  dropped  last_flush_ms  flush_errors
metric_session_count                123      123        0             32             0
metric_token_usage                12834    12834        0             45             0
event_user_prompt                   823      820        0             18             0
...
```

> 不引入 Prometheus 客户端 / OpenMetrics 格式。一来本服务自己就是监控，套娃没必要；二来纯文本足够调试，需要时再加。

配置补充：

```yaml
stats:
  listen: "127.0.0.1:9100"
  enable_pprof: true
```

### 3. 配置文件示例完善

`config.example.yaml` 补齐所有字段 + 注释：

```yaml
server:
  grpc_listen: "0.0.0.0:4317"

storage:
  duckdb_path: "./data/monitor.duckdb"

ingest:
  batch_size: 500              # 单 buffer 满 N 行立即 flush
  flush_interval: "5s"         # 至少每 5s flush 一次
  buffer_hard_limit: 50000     # 超过则丢最旧

capture:
  enabled: false               # 仅采集样本时开启
  dir: "./captured"

stats:
  listen: "127.0.0.1:9100"
  enable_pprof: true

logging:
  level: "info"                # debug / info / warn / error
  format: "json"               # json / text
```

### 4. README

`README.md` 至少包含：

1. 项目简介（一段话）
2. **架构图**（ASCII）
3. **快速启动**（带 Claude Code 环境变量）
4. **配置说明**（贴 config.example.yaml + 字段表）
5. **数据查询示例**（5–6 条常用 SQL）
6. **排查指南**：
   - 服务能收到数据但表为空 → 看日志中的 unknown / errors
   - 数据延迟 → 调小 flush_interval
   - DuckDB 文件膨胀 → `PRAGMA force_checkpoint`，或 v2 上归档
7. 链接到 `docs/protocol.md` 与 `docs/models.md`

### 5. 冒烟测试脚本

`scripts/smoke_test.sh`（或 `scripts/smoke/main.go` 用 Go 写更稳）：

流程：
1. 启动 server（背景进程，临时 duckdb 路径）
2. 用 `grpc.Dial(":4317")` 连接，构造**预定义的 OTLP payload**（不依赖真 Claude Code）：
   - 1 个 session.count fresh
   - 2 个 token.usage（input + output）
   - 1 个 cost.usage
   - 1 个 user_prompt
   - 1 个 api_request
   - 1 个 tool_result accept
3. 等待 `flush_interval + 2s`
4. 用 `database/sql` 连 DuckDB 查询计数，断言每张目标表 ≥ 期望行数
5. SIGTERM 关掉 server，确认无 panic

`scripts/smoke/payloads.go` 内嵌测试 payload（可以从 P2 采集的 `.pb` 选几个稳定样本固化）。

退出码：失败非 0，便于 CI。

### 6. 24h 稳定性验证

不写成自动化测试，作为发布前**人工 checklist**：

```bash
# 启动并把 Claude Code 接入正常工作环境
./bin/server -config config.yaml
# 24 小时后：
# 1. ps -o rss= 内存稳定（不超过启动后 +20%）
# 2. /internal/stats 中 flush_errors == 0
# 3. duckdb 文件大小合理（按你预期用量估算，例如重度用户 100 ~ 500 MB / 天）
# 4. SELECT MAX(ts) FROM metric_token_usage; 时间在 1 分钟内
# 5. go tool pprof -top http://127.0.0.1:9100/debug/pprof/heap
#    无明显泄漏（顶部对象多为 DuckDB 内部）
```

---

## 关键技术决策

| 决策点 | 选择 | 理由 |
|---|---|---|
| 监控格式 | 纯文本 | 自己是监控服务，套 Prometheus 是套娃 |
| pprof | 启用 | Go 标准能力，零依赖 |
| 冒烟方式 | Go 编写，内嵌 payload | shell + grpcurl 难维护 protobuf |
| 24h 测试 | 人工 checklist | 自动化 CI 没必要跑那么久；发布前过一次足够 |

---

## 验收标准

```bash
# 1. 日志切换
LOGGING_FORMAT=text ./bin/server -config config.yaml  # （改 config 也行）
# 期望：人类可读输出
LOGGING_FORMAT=json ./bin/server -config config.yaml
# 期望：每行合法 JSON

# 2. 自监控端点
curl http://127.0.0.1:9100/internal/healthz       # "ok"
curl http://127.0.0.1:9100/internal/stats         # 表格输出
curl http://127.0.0.1:9100/debug/pprof/heap > /tmp/heap.pprof
go tool pprof -top /tmp/heap.pprof

# 3. 冒烟测试
go run ./scripts/smoke
# 期望：exit 0，输出 "all assertions passed"

# 4. README 校验
# 让团队里没接触过项目的人按 README 步骤跑一遍，30 分钟内能查到数据

# 5. 24h checklist 全部通过
```

---

## 不在本阶段范围

- HTTP 查询 API（v2）
- Prometheus / OpenMetrics 导出（不做）
- 多租户、鉴权（v2 或更晚）
- 数据归档 / TTL / parquet 落地（v2 或更晚）
- 性能调优到极限（按 P4 默认配置够用）

---

## 留给 v2 的接口

- `BufferedWriter` 与 DuckDB 的边界已稳定，v2 加 `/api/query` 直接读同一个 DB 文件
- `internal/stats` 端口与 gRPC 端口分离，v2 加查询 API 时复用独立 HTTP 端口（建议另起一个 `:8080`）

---

## 开放问题

- 24h 测试期间是否要把 capture 开着？建议**关闭**，否则 `.pb` 文件会堆积。捕获能力只在采样阶段用
- 日志默认级别：`info`。如果觉得太吵（每个 Export 一条），P5 可改为 `info` 只打启动 / 关闭 / 错误，dispatcher summary 改 `debug`
- pprof 端点是否需要鉴权？本地端口（127.0.0.1）足够，不暴露公网即可
