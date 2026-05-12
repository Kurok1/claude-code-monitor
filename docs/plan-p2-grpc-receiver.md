# P2 — gRPC OTLP 接收端 + 原始 payload 采集

## 项目背景

`claude-code-monitor` 是一个 Go 实现的 Claude Code 监控服务，接收 Claude Code 客户端通过 OTLP gRPC 推送的指标 (Metrics) 与事件 (Events)，落库到 DuckDB。

本阶段在整体开发路径中的位置：P1 → **P2（当前） → P3 → P4 → P5**。

参考文档：
- `docs/protocol.md` — 19 个指标 / 事件的 OTLP 数据结构
- `docs/plan-p1-foundation.md` — 工程骨架与配置已就绪

---

## 本阶段目标

把 gRPC 接收端真正跑起来并能稳定接收 Claude Code 的数据，**但本阶段不入库**，专注两件事：

1. 实现 OTLP gRPC 协议端点（`MetricsService` 与 `LogsService`）
2. 把每个 Export 请求的**原始 protobuf 字节**按时间戳 dump 到磁盘，为 P3 的解析器测试提供真实样本

完成后用户（项目维护者）会启动本服务并把 Claude Code 接进来，跑一段时间采集多样本（不同模型、不同工具、不同会话），P3 直接用这些样本做 golden testdata。

---

## 前置条件

- P1 已完成：`*store.DB` 可用、`config.Config` 加载正常、19 张表已建（即使本阶段不写）
- 测试用 Claude Code 客户端可用

---

## 交付物

### 1. gRPC server 启动

`internal/otlp/server.go`：

```go
type Server struct {
    grpcServer *grpc.Server
    listener   net.Listener
    metricsSvc *MetricsService
    logsSvc    *LogsService
    capture    *Capturer       // 可选，依赖配置
}

func NewServer(cfg config.Config, db *store.DB) (*Server, error)
func (s *Server) Start() error           // 阻塞，直到 Stop
func (s *Server) GracefulStop()
```

main.go 集成（替换 P1 的 `waitForSignal`）：

```go
srv, err := otlp.NewServer(cfg, db)
go func() {
    if err := srv.Start(); err != nil { slog.Error("grpc server", "err", err); os.Exit(1) }
}()
waitForSignal()
srv.GracefulStop()
```

### 2. MetricsService 实现

`internal/otlp/metrics_service.go`：

```go
type MetricsService struct {
    metricspb.UnimplementedMetricsServiceServer
    log      *slog.Logger
    capture  *Capturer  // 可为 nil
}

func (s *MetricsService) Export(
    ctx context.Context,
    req *metricspb.ExportMetricsServiceRequest,
) (*metricspb.ExportMetricsServiceResponse, error) {
    // 1. 可选：原始 payload dump
    if s.capture != nil {
        s.capture.SaveMetrics(req)
    }

    // 2. 日志摘要：遍历 ResourceMetrics → ScopeMetrics → Metric
    //    统计每个 metric.name 的数据点数
    summary := summarizeMetrics(req)
    s.log.Info("metrics received",
        "resource_count", len(req.ResourceMetrics),
        "summary", summary,
    )

    // 3. 始终返回成功（partial_success 留空）
    return &metricspb.ExportMetricsServiceResponse{}, nil
}
```

> `summarizeMetrics` 返回 `map[string]int`：metric 名 → 数据点总数。

### 3. LogsService 实现

`internal/otlp/logs_service.go`：

结构与 MetricsService 对称。

```go
func (s *LogsService) Export(
    ctx context.Context,
    req *logspb.ExportLogsServiceRequest,
) (*logspb.ExportLogsServiceResponse, error) {
    if s.capture != nil {
        s.capture.SaveLogs(req)
    }

    // 按 attributes["event.name"] 分组计数
    summary := summarizeEvents(req)
    s.log.Info("logs received",
        "resource_count", len(req.ResourceLogs),
        "summary", summary,
    )
    return &logspb.ExportLogsServiceResponse{}, nil
}
```

### 4. 原始 payload 采集

`internal/otlp/capture.go`：

```go
type Capturer struct {
    dir string
}

func NewCapturer(cfg config.CaptureConfig) (*Capturer, error) {
    if !cfg.Enabled { return nil, nil }
    // mkdir -p <dir>/metrics  <dir>/logs
    return &Capturer{dir: cfg.Dir}, nil
}

func (c *Capturer) SaveMetrics(req *metricspb.ExportMetricsServiceRequest) {
    b, _ := proto.Marshal(req)
    name := fmt.Sprintf("%s/metrics/%s.pb",
        c.dir, time.Now().UTC().Format("20060102T150405.000000000"))
    _ = os.WriteFile(name, b, 0644)
}

func (c *Capturer) SaveLogs(req *logspb.ExportLogsServiceRequest) { /* 对称 */ }
```

设计要点：
- **失败不影响响应**：dump 失败仅记 warn 日志，仍返回成功
- 文件名带纳秒时间戳，单进程内不会冲突
- 文件直接是 protobuf 序列化字节，P3 用 `proto.Unmarshal` 还原
- 配置 `capture.enabled=false`（默认）时整个 Capturer 为 nil，零开销

### 5. 优雅关闭

- 收到 `SIGINT` / `SIGTERM` 后：`grpcServer.GracefulStop()` 等待正在处理的 RPC 完成
- 设置 30s 超时上限，超过则 `Stop()` 强制
- `defer db.Close()` 在 main 末尾

---

## 关键技术决策

| 决策点 | 选择 | 理由 |
|---|---|---|
| 传输协议 | 仅 gRPC | 已在路径规划中明确不支持 HTTP/protobuf |
| TLS | 不启用 | 本地服务，先简化；后续如需可读 `capture` 一样从配置加 |
| 鉴权 | 不做 | 同上 |
| Partial Success | 不使用 | 协议允许，但简化版本永远全成功 |
| 入库 | 本阶段**不入库** | 先验证协议联通 + 采集样本，落库放 P4 |
| 原始 payload 序列化 | 直接 `proto.Marshal` 后写文件 | 还原成 `Request` 对象最简单；不需要 JSON 转换 |
| Capture 默认 | 关闭 | 生产环境不要无限堆积文件，仅采样时开启 |

---

## 验收标准

```bash
# 1. 启动（capture 开启）
cat > config.yaml <<EOF
server:
  grpc_listen: "0.0.0.0:4317"
storage:
  duckdb_path: "./data/monitor.duckdb"
capture:
  enabled: true
  dir: "./captured"
logging:
  level: "info"
  format: "text"
EOF
./bin/server -config config.yaml

# 2. 另起一个终端，让 Claude Code 接进来
export CLAUDE_CODE_ENABLE_TELEMETRY=1
export OTEL_METRICS_EXPORTER=otlp
export OTEL_LOGS_EXPORTER=otlp
export OTEL_EXPORTER_OTLP_PROTOCOL=grpc
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317
export OTEL_METRIC_EXPORT_INTERVAL=10000
export OTEL_LOGS_EXPORT_INTERVAL=5000
claude          # 任意正常使用一段时间

# 3. 观察服务日志
# 期望：
# - 每 10s 看到 metrics received，summary 含 claude_code.token.usage / cost.usage 等
# - 每 5s 看到 logs received，summary 含 user_prompt / api_request / tool_result 等

# 4. 检查 capture 目录
ls -lh captured/metrics/ captured/logs/ | head
# 期望：多个 .pb 文件，每个文件 < 100KB

# 5. 优雅退出
kill -TERM <pid>
# 期望：日志 "graceful stop done"，进程退出
```

---

## 不在本阶段范围

- OTLP payload 解析为强类型行结构体（P3）
- 写入 DuckDB（P4）
- TLS、鉴权、速率限制（v2 或更晚）
- HTTP/protobuf 端点（已确定不做）
- Capture 文件轮转 / 清理 —— 本阶段就是采样工具，手动清即可

---

## 留给后续阶段的接口

- `*otlp.Server` 在 P3 中被注入 `*Dispatcher`（替换当前的"只打日志"逻辑）
- `captured/` 目录中的 `.pb` 文件是 P3 testdata 的来源

P3 改造时点：
```go
// P3 会替换为：
// summary, rows := s.dispatcher.DispatchMetrics(req)
// s.log.Debug("dispatched", "summary", summary)
// （rows 仍不落库，等 P4）
```

---

## 开放问题

- 是否需要在 capture 文件名里加上请求大小、来源 IP？建议**先不加**，P3 用得到再补
- Claude Code OTLP 客户端 gRPC 默认是否带 keepalive？需要服务端 `grpc.KeepaliveParams` 调优吗？先用默认值跑，出问题再调
- 采集多长时间够 P3 用？建议至少覆盖：
  - 1 次 fresh session、1 次 resume、1 次 continue
  - 至少 2 个不同 model
  - tool_result 含 accept / reject 各一例
  - compaction（长会话触发或手动 `/compact`）
  - api_error（断网模拟）
