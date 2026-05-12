# P1 — 工程骨架 + YAML 配置 + DuckDB + 迁移

## 项目背景

本项目 `claude-code-monitor` 是一个 Go 实现的 Claude Code 监控服务：
- **输入**：Claude Code 客户端通过 OTLP gRPC 推送的指标 (Metrics) 与事件 (Events)
- **存储**：DuckDB 单文件，每个指标 / 事件独立一张表，共 **19 张表**
- **输出**（v2 才做）：HTTP 查询 API + 前端聚合看板

本阶段在整体开发路径中的位置：**P1（当前） → P2 → P3 → P4 → P5**。

参考文档：
- `docs/protocol.md` — 19 个指标 / 事件的 OTLP 数据结构
- `docs/models.md` — 19 张 DuckDB 表的 DDL

---

## 本阶段目标

为后续接收端、解析器、写入器搭好可运行的工程骨架：
1. Go 模块、目录结构、依赖确定下来
2. YAML 配置加载机制可用
3. DuckDB 连接 + 迁移框架就位
4. 19 张表通过迁移自动建好

完成后服务可以"空跑"——启动后 `.duckdb` 文件里 19 张表都存在，但还没有 gRPC 端点（P2 加）。

---

## 前置条件

- 本地 Go 工具链（建议 1.22+）
- macOS / Linux（DuckDB CGO 在这两个平台稳定）

---

## 交付物

### 1. Go 模块初始化

```bash
go mod init github.com/kuroky/claude-code-monitor
```

依赖（明确版本待 P1 实施时定，下面是建议）：

| 包 | 用途 |
|---|---|
| `google.golang.org/grpc` | P2 用，提前引入 |
| `go.opentelemetry.io/proto/otlp` | OTLP protobuf 定义 |
| `github.com/marcboeker/go-duckdb` | DuckDB 驱动（CGO） |
| `gopkg.in/yaml.v3` | YAML 配置解析 |
| 标准库 `log/slog` | 结构化日志 |

> 不引入 koanf / viper —— 单一 YAML 配置源用 `yaml.v3` 直接 Unmarshal 到强类型 struct 即可。

### 2. 目录结构

```
claude-code-monitor/
├── cmd/
│   └── server/
│       └── main.go               # 入口：load config → init db → start server
├── internal/
│   ├── config/
│   │   └── config.go             # YAML 解析 + 默认值
│   ├── store/
│   │   ├── db.go                 # DuckDB 连接封装
│   │   ├── migrate.go            # 迁移运行器
│   │   └── migrations/
│   │       ├── 001_schema_migrations.sql
│   │       ├── 002_metric_tables.sql        # 8 张 metric 表
│   │       └── 003_event_tables.sql         # 11 张 event 表
│   ├── otlp/                     # P2/P3 填充
│   └── ingest/                   # P4 填充
├── config.example.yaml
├── docs/                         # 已有文档
├── go.mod
└── go.sum
```

### 3. YAML 配置 schema

`config.example.yaml`：

```yaml
server:
  grpc_listen: "0.0.0.0:4317"

storage:
  duckdb_path: "./data/monitor.duckdb"

ingest:                          # P4 才生效，但提前留位
  batch_size: 500
  flush_interval: "5s"
  buffer_hard_limit: 50000

capture:                         # P2 才生效，但提前留位
  enabled: false
  dir: "./captured"

logging:
  level: "info"                  # debug / info / warn / error
  format: "json"                 # json / text
```

对应 `internal/config/config.go`：

```go
type Config struct {
    Server  ServerConfig  `yaml:"server"`
    Storage StorageConfig `yaml:"storage"`
    Ingest  IngestConfig  `yaml:"ingest"`
    Capture CaptureConfig `yaml:"capture"`
    Logging LoggingConfig `yaml:"logging"`
}

type ServerConfig struct {
    GRPCListen string `yaml:"grpc_listen"`
}

type StorageConfig struct {
    DuckDBPath string `yaml:"duckdb_path"`
}

type IngestConfig struct {
    BatchSize       int           `yaml:"batch_size"`
    FlushInterval   time.Duration `yaml:"flush_interval"`
    BufferHardLimit int           `yaml:"buffer_hard_limit"`
}

type CaptureConfig struct {
    Enabled bool   `yaml:"enabled"`
    Dir     string `yaml:"dir"`
}

type LoggingConfig struct {
    Level  string `yaml:"level"`
    Format string `yaml:"format"`
}

func Load(path string) (Config, error) { /* read + unmarshal + apply defaults + validate */ }
```

- 入参支持 `-config <path>` flag，默认 `./config.yaml`
- 缺字段使用默认值（在 `applyDefaults` 中显式赋）
- 校验失败时 fail-fast：返回带具体字段名的 error

### 4. DuckDB 连接

`internal/store/db.go`：

```go
type DB struct {
    conn *sql.DB
    path string
}

func Open(cfg config.StorageConfig) (*DB, error) {
    // sql.Open("duckdb", cfg.DuckDBPath)
    // SetMaxOpenConns(1)  // DuckDB 是单写者；多读 OK
    // 建目录、ping、返回
}

func (d *DB) Close() error
```

> DuckDB 不能跨进程并发写。`SetMaxOpenConns(1)` 配合应用层串行化 flush（P4 处理）。

### 5. 迁移框架

`internal/store/migrate.go`：

```go
type Migration struct {
    Version int
    Name    string
    SQL     string
}

// 启动时调用
func RunMigrations(db *DB, migrations []Migration) error
```

- `001_schema_migrations.sql` 内容：
  ```sql
  CREATE TABLE IF NOT EXISTS schema_migrations (
      version    INTEGER PRIMARY KEY,
      name       VARCHAR NOT NULL,
      applied_at TIMESTAMP NOT NULL DEFAULT now()
  );
  ```
- migrations 用 `//go:embed migrations/*.sql` 编进二进制
- 按 `version` 升序对比 `schema_migrations` 表，缺失的依次执行 + 记录
- 同一版本号下整文件多语句一次性执行（DuckDB 支持分号分隔的多语句）

### 6. 19 张表 SQL

`002_metric_tables.sql` —— 8 张表的 CREATE TABLE，直接复制 `docs/models.md` §2 的 DDL。
`003_event_tables.sql` —— 11 张表的 CREATE TABLE，直接复制 `docs/models.md` §3 和 §4 的 DDL。

### 7. main.go 流程

```go
func main() {
    cfg := mustLoadConfig()
    setupLogger(cfg.Logging)
    db := mustOpenDB(cfg.Storage)
    defer db.Close()
    if err := store.RunMigrations(db, AllMigrations); err != nil { ... }

    slog.Info("server ready (P1 skeleton)", "duckdb", cfg.Storage.DuckDBPath)
    // P2 之后这里启动 gRPC server；P1 阶段允许直接退出或 block 等信号
    waitForSignal()
}
```

---

## 关键技术决策

| 决策点 | 选择 | 理由 |
|---|---|---|
| 配置库 | `yaml.v3` 直接 Unmarshal | 单一来源够用，避免引入 koanf / viper |
| 配置覆盖 | 暂不支持 ENV 覆盖 | YAGNI；本地工具，文件改了重启即可 |
| 迁移嵌入 | `go:embed` SQL 文件 | 单二进制部署友好 |
| 迁移粒度 | 一类表一个文件（metric / event） | 19 张表分开太碎，按 P3/P4 演进时再细化 |
| DuckDB 连接 | `sql.DB` + MaxOpenConns=1 | 简单可控；后续 Appender API 单独走 driver 连接 |
| Go 版本 | 1.22+ | `log/slog` 自带，CGO 兼容 DuckDB |

---

## 验收标准

```bash
# 1. 编译
go build -o bin/server ./cmd/server

# 2. 首次启动
./bin/server -config config.example.yaml
# 日志可见：migrations applied 1..3

# 3. 验证表
duckdb ./data/monitor.duckdb "SELECT table_name FROM duckdb_tables() ORDER BY table_name;"
# 期望输出 19 张表 + schema_migrations 共 20 行

# 4. 幂等
./bin/server -config config.example.yaml
# 日志可见：no pending migrations，无报错

# 5. 错误路径
./bin/server -config /nonexistent.yaml
# 失败退出，错误信息包含 "config file"
```

---

## 不在本阶段范围

- gRPC 服务（P2）
- payload 解析（P3）
- 数据写入（P4）
- 日志输出格式细化（P5 收尾）
- 任何 HTTP 端点（v2）
- ENV 配置覆盖、热更新

---

## 留给后续阶段的接口

- `*store.DB` 给 P4 Appender 使用
- `config.Config` 全量传给 P2/P4/P5
- `internal/otlp/` 目录已建空，P2 直接填

---

## 开放问题

- DuckDB 文件目录是否要主动 `mkdir -p`？建议 **是**，避免首次启动失败
- 迁移失败回滚策略？建议 **不回滚**：DuckDB DDL 不在事务里，失败就报错退出，开发者手动清表重试（这一版没有线上数据）
