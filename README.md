# claude-code-monitor

Go 实现的 Claude Code 监控服务。接收 Claude Code 通过 **OTLP gRPC** 推送的 Metrics 与 Events，落到本地 DuckDB（一指标 / 一事件 = 一张表，共 **19 张表**），便于后续做聚合分析与可视化。

> 当前是 v1，只有后端 ingest + 落库；查询 API + 前端看板规划在 v2。

---

## 架构

```
Claude Code (gRPC)
        │  :4317
        ▼
[OTLP MetricsService / LogsService]
        │
        ▼
[Dispatcher]  → 按 metric.name / event.name 路由到 19 个强类型 row 结构体
        │
        ▼
[BufferedWriter]  → 每张表独立 buffer + DuckDB Appender
        │            按 batch_size 或 flush_interval 触发
        ▼
   DuckDB 单文件
```

更深的设计依据见：
- [`docs/protocol.md`](docs/protocol.md) — OTLP 指标 / 事件字段规范
- [`docs/models.md`](docs/models.md) — DuckDB 表结构 + 写入要点
- [`CLAUDE.md`](CLAUDE.md) — 项目开发规范与不变量

---

## 快速开始

### 1. 构建

```bash
go build -o bin/server ./cmd/server
```

依赖 CGO（go-duckdb），首次构建会拉取 DuckDB 静态库。

### 2. 启动 server

复制配置示例并按需修改：
```bash
cp config.example.yaml config.yaml
./bin/server -config config.yaml
```

或者一键脚本（同时打开 OTLP payload 采集，方便回归 / 抓样本）：
```bash
./scripts/run-capture.sh
```

启动后会看到：
```
buffered writer ready  tables=19
stats server listening addr=127.0.0.1:9100
grpc server listening  addr=127.0.0.1:4317
```

### 3. 把 Claude Code 指向本服务

在另一个终端：
```bash
source scripts/claude-env.sh
claude
```

`claude-env.sh` 内容：
```bash
export CLAUDE_CODE_ENABLE_TELEMETRY=1
export OTEL_METRICS_EXPORTER=otlp
export OTEL_LOGS_EXPORTER=otlp
export OTEL_EXPORTER_OTLP_PROTOCOL=grpc
export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4317
export OTEL_METRIC_EXPORT_INTERVAL=10000   # 调试可短，生产改回 60000
export OTEL_LOGS_EXPORT_INTERVAL=5000
```

### 4. 查询

```bash
duckdb data/monitor.duckdb "SELECT table_name FROM duckdb_tables() ORDER BY 1;"

# Token 用量（按模型 + 类型）
duckdb data/monitor.duckdb "
  SELECT model, type, SUM(value) AS tokens
  FROM metric_token_usage
  WHERE ts >= now() - INTERVAL 1 DAY
  GROUP BY 1, 2 ORDER BY 1, 2;"

# 当日成本（USD）
duckdb data/monitor.duckdb "
  SELECT model, ROUND(SUM(value), 4) AS usd
  FROM metric_cost_usage
  WHERE ts >= now() - INTERVAL 1 DAY
  GROUP BY 1 ORDER BY 2 DESC;"

# 工具调用接受 / 拒绝比
duckdb data/monitor.duckdb "
  SELECT tool_name, decision_type, COUNT(*) FROM event_tool_result
  GROUP BY 1, 2 ORDER BY 1, 2;"

# 用 prompt.id 串起一个 prompt 全周期
duckdb data/monitor.duckdb "
  SELECT 'prompt' AS evt, ts FROM event_user_prompt WHERE prompt_id = '<UUID>'
  UNION ALL
  SELECT 'api'    , ts FROM event_api_request WHERE prompt_id = '<UUID>'
  UNION ALL
  SELECT 'tool'   , ts FROM event_tool_result WHERE prompt_id = '<UUID>'
  ORDER BY ts;"
```

未识别的 attribute 都落在每张表的 `attrs` 列（VARCHAR，内容是 JSON 文本），需要时用 `json_extract_string(attrs, '$."key.name"')` 提取。

---

## 用 Claude Code Hook 自动启动（可选）

不想每次手动开 server，可以挂到 Claude Code 的 SessionStart / SessionResume 钩子。仓库里提供了一个幂等脚本 `scripts/hook-session-start.sh`：缺二进制会自动 `go build`，缺 `config.yaml` 会从 `config.example.yaml` 复制，最后用 `nohup` 后台拉起 server。**server 自己有 preflight：gRPC 端口已被占用就在 ~14ms 内 exit 0**，所以重复触发完全安全。

在 `~/.claude/settings.json` 里加入：

```json
{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "/绝对路径/to/claude-code-monitor/scripts/hook-session-start.sh"
          }
        ]
      }
    ]
  }
}
```

可用环境变量覆盖默认路径（在 hook 命令前 `MONITOR_CONFIG=... MONITOR_LOG=... /path/to/hook-session-start.sh`）：

| 变量 | 默认 | 说明 |
|---|---|---|
| `MONITOR_CONFIG` | `<repo>/config.yaml` | 配置文件路径 |
| `MONITOR_LOG`    | `/tmp/claude-code-monitor.log` | server stdout/stderr 重定向到此 |

排查：
```bash
tail -f /tmp/claude-code-monitor.log               # server 日志
pgrep -lf 'bin/server'                              # 当前实例
curl -s http://127.0.0.1:9100/internal/stats       # 累计指标
pkill -TERM -f 'bin/server -config'                # 手动停
```

注意：preflight 只 probe `grpc_listen` 端口，不区分占用者身份。若 4317 被别的进程占了（其它 OTLP collector 等），hook 也会静默退出 0，server 日志里会有 `another instance appears to be listening` 一行可循。

---

## 配置项（`config.yaml`）

| 段 | 字段 | 默认 | 说明 |
|---|---|---|---|
| `server` | `grpc_listen` | `0.0.0.0:4317` | OTLP gRPC 监听地址 |
| `storage` | `duckdb_path` | `./data/monitor.duckdb` | DuckDB 文件路径，父目录会自动创建 |
| `ingest` | `batch_size` | `500` | 单 buffer 满 N 行立即 flush |
| `ingest` | `flush_interval` | `5s` | 至少每 N 秒 flush |
| `ingest` | `buffer_hard_limit` | `50000` | 超过则丢最旧 + 计数 |
| `capture` | `enabled` | `false` | 开启后原始 OTLP protobuf 字节落盘到 `dir`，用于 P3 testdata 或调试 |
| `capture` | `dir` | `./captured` | 采样目录 |
| `stats` | `listen` | `127.0.0.1:9100` | HTTP 自监控端口，留空禁用 |
| `stats` | `enable_pprof` | `false` | 注册 `/debug/pprof/*`，建议本地调试时开 |
| `logging` | `level` | `info` | `debug` / `info` / `warn` / `error` |
| `logging` | `format` | `json` | `json` / `text` |

---

## 运维工具

### Stats 端点

```bash
curl http://127.0.0.1:9100/internal/healthz   # liveness
curl http://127.0.0.1:9100/internal/stats     # per-table buffer 计数
```

`/internal/stats` 输出（节选）：
```
# claude-code-monitor stats
uptime_seconds        342

# per-table buffers
table                                appended    flushed    dropped flush_errors    pending
metric_token_usage                       12834      12834          0            0          0
event_user_prompt                          823        820          0            0          3
...
```

启用 pprof 时（`enable_pprof: true`）：
```bash
go tool pprof http://127.0.0.1:9100/debug/pprof/heap
```

### 检查采集的原始 OTLP 样本

```bash
./scripts/inspect.sh -aggregate captured/        # 汇总：每种 metric / event 出现次数
./scripts/inspect.sh captured/metrics/<file>.pb  # 单文件 summary
./scripts/inspect.sh -format json captured/logs/<file>.pb | jq .
```

### 回放采集的样本（无需启 Claude Code）

```bash
./scripts/replay.sh captured/
```

### 一键冒烟测试

```bash
./scripts/smoke.sh
```

会在临时目录起一个空 server，发已知 OTLP 请求，SIGTERM 后 SELECT 验证 7 张表都有数据。CI 友好。

---

## 排查

| 现象 | 排查方向 |
|---|---|
| 服务收到数据，但所有表为空 | 看 `dispatched` 日志中的 `unknown` / `errors` 字段；Claude Code 升级可能引入新 metric / event |
| 数据延迟入库 | 调小 `ingest.flush_interval`；或检查 `/internal/stats` 中 `pending` 是否堆积 |
| `flush_errors` 非零 | 看 server 日志 ERROR；通常是磁盘满或文件损坏 |
| 重启后 `attrs` 抽不出字段 | 老版本 `attrs` 列曾用 `JSON` 类型导致双重转义；当前是 `VARCHAR`，旧数据需清表重新写入 |
| `.duckdb` 文件不收敛 | `duckdb data/monitor.duckdb "PRAGMA force_checkpoint;"` 手工强制 checkpoint |

---

## 开发

```bash
go test ./...
go test -race ./...
go vet ./...
gofmt -w .
```

更多规范见 [`CLAUDE.md`](CLAUDE.md)。
