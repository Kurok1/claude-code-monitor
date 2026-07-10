# 设计:token 生产速率展示 + 价目表展示

- 日期:2026-07-09
- 状态:已评审通过,待实现
- 目标版本:v2.5.0
- 前置基线:
  - `2026-07-02-codex-dashboard-unification-design.md`(dashboard 查询层统一,client 筛选器)
  - `2026-07-02-third-party-cost-estimation-design.md`(`internal/pricing` 计价引擎,LiteLLM 计价表)

---

## 1. 背景与目标

dashboard 目前展示 token 总量、成本、缓存命中率、请求数,但没有任何**速率类**指标,也没有把系统内置的 LiteLLM 计价表暴露给用户。本设计新增两个功能:

1. **token 生产速率**:两个互补指标——
   - **生成速度(tok/s)**:模型/服务响应快慢,按请求耗时归一;
   - **吞吐率(tok/min)**:单位墙钟时间的 token 产量,反映使用强度的波峰波谷。
2. **价目表展示**:把计价引擎维护的单价表,以「实际出现过的模型」为范围展示成表格。

数据可行性已在 dev 库(`data/monitor.dev.duckdb`,2026-05-12 ~ 06-10,11,394 条 `event_api_request`)验证:全部行同时具备 `duration_ms > 0` 与 `output_tokens > 0`,实测中位生成速度 opus-4-8 ≈ 49 tok/s、haiku-4.5 ≈ 40 tok/s,数值合理。

---

## 2. 已拍板决策

| 决策 | 选择 | 理由 |
|---|---|---|
| 速率指标范围 | **生成速度 + 吞吐率两个都做** | 两者数据源独立、含义互补,共享一个端点与前端区块,边际成本低 |
| 客户端覆盖 | **Claude + Codex 都覆盖**,沿用双臂查询模式 | `codex_event_token_usage` 同时有 `duration_ms` 和 token 数,两个指标都能算 |
| 价目表范围 | **只展示数据中实际出现过的模型** | 直接回答「我用的模型多少钱」;全量 ~2,949 条信息密度过低 |
| 总体架构 | **方案 A:两个独立新端点**(`/api/usage/rates`、`/api/pricing/models`),不动现有端点 | 与现有 6 端点结构一致,改动边界清晰;拒绝并入 snapshot(类型膨胀)与前端聚合(违背后端聚合分层) |
| 生成速度聚合 | **加权平均** `SUM(output_tokens)*1000/SUM(duration_ms)`,不用中位数 | 分子分母可在 Go 侧跨模型组无损合并,与 Classifier 折叠模式兼容;中位数不可合并 |
| 分桶粒度 | **滑动窗口细粒度**:day→48×1h、week→28×6h、month→30×1d,不复用 trends 的 14d/12w/12m 日历桶 | 吞吐率的价值在波峰波谷,日粒度会抹平峰值 |
| 吞吐率堆叠口径 | 按 token 类型四层堆叠;Codex 投影沿用 `QueryPeriodTokens` 先例(input→`input-cached`、cacheRead→`cached`、cacheCreation→0) | 保证 `client=all` 两家语义可加,与现有 KPI 口径一致 |
| pricing 关闭态 | `pricing.enabled=false` 时端点返回 `200 {"enabled":false}`,**不为展示而加载计价表**,UI 显示引导文案 | 维持「pricing 默认关闭零影响」决策锚点 |
| 单价展示单位 | **$/1M tokens**(后端将 LiteLLM per-token 单价 ×1e6 后返回) | 行业惯用单位,避免 2.5e-6 这类可读性差的数值 |
| 速率不落库 | 两个速率均为**查询时派生**,不新增表/列,不回填 | 纯展示需求,原始数据已足够,避免 schema 变更 |

---

## 3. 指标口径

### 3.1 生成速度(tok/s)

- **定义**:时间桶内、按模型组聚合的加权平均 `SUM(output_tokens) * 1000.0 / SUM(duration_ms)`。
- **数据源**:
  - Claude 臂:`event_api_request`,过滤 `duration_ms > 0 AND output_tokens > 0`;
  - Codex 臂:`codex_event_token_usage`,过滤 `duration_ms > 0 AND output_token_count > 0`。
- **分组**:SQL 按原始 `model` 出 `SUM(output_tokens)`、`SUM(duration_ms)`,Go 侧经 `Classifier` 折叠为模型组时**分子分母各自相加再除**——加权平均可无损合并,这是选它而非中位数的根本原因。
- **KPI**:`current` = 整个图表窗口(48h/7d/30d)的整体加权平均;`previous` = 前一个等长窗口,供涨跌箭头。
- **已知口径偏差**(UI 脚注说明):`duration_ms` 是请求整体墙钟时间(含首 token 等待/排队),数值系统性略低于纯解码速度;失败请求(`event_api_error`)无 token 数,不参与计算。

### 3.2 吞吐率(tok/min)

- **定义**:时间桶内 `SUM(tokens) / 桶宽分钟数`,按 token 类型堆叠。
- **数据源与投影**:
  - Claude 臂:`metric_token_usage`(delta 值直接 SUM),四层 = `input` / `output` / `cacheRead` / `cacheCreation`,总和即全量 token(Anthropic 并列口径);
  - Codex 臂:`codex_event_token_usage`,投影为 input→`input_token_count - cached_token_count`(负值钳 0)、output→`output_token_count`、cacheRead→`cached_token_count`、cacheCreation→0(OpenAI 子集口径折算成并列口径,先例见 `queries.go` `QueryPeriodTokens` Codex 臂)。
- **归桶**:按 `ts` 归桶。Claude 的 delta 区间 `[start_ts, ts]`(默认 60s export interval)相对最小桶宽 1h 的跨桶涂抹 ≤ 1/60,可接受,不做区间拆分。
- **末桶部分归一**:当前未走完的桶按**实际流逝分钟数**(`now - bucket_start`)归一,避免末桶被 60 分钟满额分母稀释。

### 3.3 时间桶

| range | 窗口 | 桶宽 | 桶数 |
|---|---|---|---|
| `day` | 最近 48h(滑动) | 1h | 48 |
| `week` | 最近 7d(滑动) | 6h | 28 |
| `month` | 最近 30d(滑动) | 1d | 30 |

- SQL 统一用 DuckDB `time_bucket(INTERVAL, ts, origin)`;6h/1d 桶以**本地时区(config `dashboard.timezone`)零点对应的 UTC 瞬间**为 origin 对齐(0/6/12/18 点边界符合直觉),1h 桶天然对齐。
- 窗口起点 = `now` 向前推满窗口后**向下取整到桶边界**;谓词沿用半开区间 `ts >= ? AND ts < ?`。
- **空桶补齐在 Go 侧**(沿用现有 gap-fill 惯例):生成速度空桶为 `null`(前端断线),吞吐率空桶为 0。

---

## 4. API 设计

### 4.1 `GET /api/usage/rates?range=day|week|month&client=all|claude|codex`

```json
{
  "bucket_interval": "1h",
  "speed": {
    "groups": ["Claude Opus", "Claude Haiku"],
    "points": [
      { "ts": "2026-07-09T04:00:00Z", "values": { "Claude Opus": 49.1, "Claude Haiku": 40.2 } }
    ],
    "current": 48.7,
    "previous": 51.2
  },
  "throughput": {
    "types": ["input", "output", "cache_read", "cache_creation"],
    "points": [
      { "ts": "2026-07-09T04:00:00Z", "values": { "output": 1234.5, "input": 320.0, "cache_read": 88000.0, "cache_creation": 2100.0 } }
    ]
  }
}
```

- `ts` 为 RFC3339 UTC(与既有 `time.Time` JSON 序列化一致),前端负责本地化格式(1h/6h 桶显示 `HH:00`,1d 桶显示 `MM-dd`)。
- `speed.groups` 即图例顺序,按窗口内 output token 总量倒序(与 trends 的 groups 排序惯例一致)。
- `speed.points[].values` 中某组该桶无数据则**省略该 key**(即 null 语义);`throughput` 空桶补 0。
- 数值不在后端四舍五入,前端格式化。
- 默认值与现有端点一致:`range` 缺省 `day`,`client` 缺省 `all`。

### 4.2 `GET /api/pricing/models?client=all|claude|codex`

```json
{
  "enabled": true,
  "table_entries": 2949,
  "last_refresh": "2026-07-09T00:00:00Z",
  "models": [
    {
      "model": "claude-opus-4-8",
      "clients": ["claude"],
      "matched": true,
      "input_per_1m": 15.0,
      "output_per_1m": 75.0,
      "cache_read_per_1m": 1.5,
      "reasoning_output_per_1m": null,
      "requests": 4472,
      "last_seen": "2026-06-10T01:07:50Z"
    }
  ]
}
```

- `enabled=false` 时:`200 {"enabled": false, "models": []}`(非错误),`table_entries` / `last_refresh` 省略。
- 单价字段为 `*float64`(JSON null = LiteLLM 该字段缺失);`matched=false` 表示模型出现在数据里但计价表查不到,四个单价字段均为 null。
- `models` 按 `last_seen` 倒序;`client` 参数按来源臂过滤。
- `table_entries` / `last_refresh` 来自现有 `Engine.Stats()`。

---

## 5. 后端实现

### 5.1 `internal/pricing`:新增只读查价方法

```go
// PriceFor 返回 model 的单价条目(经 exact→normalized 两级匹配,与 CostFor 同一 lookup)。
// 引擎未启用 / 未匹配 → ok=false。
func (e *Engine) PriceFor(model string) (ModelPrice, bool)
```

复用 `priceTable.lookup` 现有匹配逻辑(exact → 去 `provider/` 前缀与 `-YYYY-MM-DD` 后缀),不新增匹配规则。`ModelPrice` 已是导出类型,无需改动。

### 5.2 `internal/dashboard`:两个新 builder

- **`rates.go`**:`BuildRates(ctx, db, client, rng, tw)` → 解析窗口/桶宽 → 调 `queries.go` 新增的双臂查询 → Classifier 折叠模型组 → Go 侧补桶 → 组装 `RatesResponse`。
- **`pricing.go`**:`BuildPricingModels(ctx, db, client, prices)` → 三表 union 查 distinct model → 逐个 `PriceFor` → 组装 `PricingModelsResponse`。
- **`queries.go`** 新增:
  - `QuerySpeedBuckets`(双臂):按 `(bucket, model)` 出 `SUM(output_tokens), SUM(duration_ms)`;
  - `QuerySpeedWindow`(双臂):整窗口分子分母,供 current/previous KPI;
  - `QueryThroughputBuckets`(双臂):按 `(bucket)` 出四类 token SUM(Codex 臂做投影);
  - `QuerySeenModels`(双臂):
    ```sql
    SELECT model, MAX(ts), COUNT(*), 'claude' FROM event_api_request   WHERE model <> '' AND model NOT LIKE '<%' GROUP BY model
    UNION ALL
    SELECT model, MAX(ts), 0,        'claude' FROM metric_token_usage  WHERE model <> '' AND model NOT LIKE '<%' GROUP BY model
    UNION ALL
    SELECT model, MAX(ts), COUNT(*), 'codex'  FROM codex_event_token_usage WHERE model <> '' GROUP BY model
    ```
    Go 侧按 model 合并:`last_seen` 取 max、`requests` 求和、`clients` 取并集(`metric_token_usage` 行只补覆盖、requests 计 0,避免与 `event_api_request` 重复计数)。
- **`types.go`** 新增 `RatesResponse` / `PricingModelsResponse` 及子结构,snake_case JSON tag。
- **`timewin.go`** 新增 rates 专用的滑动窗口解析(窗口起点、桶宽、origin 对齐),不动现有 `WindowSpec`。

### 5.3 wire-up

- `handler.go` path-switch 注册 `/api/usage/rates`、`/api/pricing/models`;非法参数走 `isUserError` → 400。
- `dashboard.NewHandler` 新增参数:消费方接口(interface 在消费方包定义原则)
  ```go
  type PriceLookup interface {
      PriceFor(model string) (pricing.ModelPrice, bool)
      Stats() pricing.Stats
  }
  ```
  `cmd/server/main.go` 把 `*pricing.Engine` 注入(disabled 时为 no-op 引擎,`PriceFor` 恒 `ok=false`,handler 以 `cfg.Pricing.Enabled` 判定 `enabled` 字段)。

---

## 6. 前端实现

- **「速率」区块**:置于 trends 图下方,两张卡片并排(窄屏纵排):
  - 生成速度:新组件 `frontend/src/components/charts/LineChart.tsx`——多系列折线、手写 SVG(参照 `StackedAreaChart` 的 viewBox/path 实现),**支持 null 断线**;图例颜色复用 `metaForGroup` 的模型组配色;卡片头部放 `current` KPI + 对 `previous` 的涨跌箭头(复用 `pctDelta`)。
  - 吞吐率:**复用现有 `StackedAreaChart`**,系列换成四种 token 类型(固定配色,与模型组配色区分);y 轴数值大,沿用现有 K/M 缩写格式化。
  - 两张图共用 rates 端点一次请求,跟随页面顶部 range 与 全部/Claude/Codex 切换。
- **「价目表」区块**:页面底部纯表格(无新图表组件),列:模型 | 客户端 | Input | Output | 缓存读 | 推理输出($/1M)| 最近使用;`matched=false` 单价列显示「未收录」;跟随 client 切换过滤;脚注两条:① Claude 实际成本以客户端自报为准,此表仅为参考价;② 生成速度含首 token 等待(此脚注在速率卡片)。`enabled=false` 时整块显示引导文案(提示 config 开启 `pricing.enabled` 并配置 `source_file`)。
- **数据层**:`frontend/src/api/dashboard.ts` 的 `Dashboard.fetch` 并发请求从 4 → 6(rates 带 range+client,pricing 带 client),`adapt()` 增加对应转换;沿用 5 分钟对齐自动刷新。

---

## 7. 错误处理

- 非法 `range` / `client` → 400 `{"error": "..."}`(沿用 `isUserError` 字符串判定);查询失败 → 500。
- 除零双重防护:SQL 层 `WHERE duration_ms > 0`;Go 层分母为 0 的桶输出 null(speed)/0(throughput)。
- `pricing.enabled=false` 是**正常业务态**(200),不是错误;engine 为 no-op 时 `PriceFor` 恒 false,handler 不判 nil。
- 空库 / 窗口内无数据:返回满长度补齐的桶数组(speed 全 null、throughput 全 0)与空 `models`,前端渲染空态。

---

## 8. 测试计划

- `internal/dashboard`(临时 DuckDB + 种子数据,沿用现有查询测试模式):
  - 加权平均跨模型组折叠正确性(两个同组 model 的分子分母合并 ≠ 两个速率的算术平均);
  - Codex 吞吐投影(`input - cached` 且负值钳 0、cacheCreation 恒 0);
  - `time_bucket` 补桶:空桶、末桶部分归一、6h 桶本地 origin 对齐;
  - `client=all/claude/codex` 三态双臂合并;
  - `QuerySeenModels` 合并逻辑(同 model 双臂并集、`<synthetic>` 过滤、metric 臂不重复计 requests);
  - pricing 端点 enabled/disabled 两态。
- `internal/pricing`:`PriceFor` 的 exact / normalized 匹配与 no-op 引擎单测。
- 全量门槛:`go test -race ./...` 全绿;`cd frontend && npm run build` 通过;`go vet` / `gofmt` 干净。

---

## 9. 非目标

- 不按 `speed`(fast mode)/ `effort` 维度切片速率(数据已留,后续按需加);
- 不做价目表历史版本、变更追踪、生效区间;
- 不回填/重算任何历史 `cost_usd`(维持 ingest 冻结决策);
- 不动 rankings 端点与现有 4 个 usage 端点的响应结构;
- 不引入前端图表库,继续手写 SVG。

---

## 10. 风险与已知偏差

| 项 | 说明 | 处置 |
|---|---|---|
| 生成速度偏低 | `duration_ms` 含 TTFT/排队 | UI 脚注说明口径 |
| Codex `ts` 偏移 | Codex `time_unix_nano` 恒 0,实际用 SDK observed time | 偏移量级秒级,对 ≥1h 桶无感 |
| delta 跨桶涂抹 | `metric_token_usage` 60s export 区间按 `ts` 归桶 | ≤ 1/60 桶宽,接受 |
| 查询串行 | 新增 2 个查询与 flush 共享单连接 | 查询轻量(聚合 + 窄窗口),沿用 30s HTTP 缓存 |
| 价目表匹配空洞 | 数据中的 model 在 LiteLLM 表无条目 | `matched=false` 显式展示「未收录」,不静默丢行 |
