# 设计:第三方模型 cost_usd 估算引擎

- 日期:2026-07-02
- 状态:已评审通过,待实现
- 前置基线:
  - `2026-07-01-codex-otel-support-design.md`(Codex 遥测落库,6 张 `codex_event_*` 表)
  - `2026-07-02-codex-dashboard-unification-design.md`(dashboard 查询层统一,client 筛选器)

---

## 1. 背景与目标

Codex(及未来其他第三方客户端)通过 OTLP 上报的遥测**不含 `cost_usd`**,导致 dashboard 无法给出这些客户端的花费金额。前两份 spec 都把「Codex 成本估算」列为**非目标**(理由:「Codex 无成本数据,不硬造」)。

本设计**显式推翻该决策**:引入一个**客户端无关的计价引擎**,按 `model` 名查外部计价表(LiteLLM 格式),在 ingest 时算出 `cost_usd` 并落列。Claude 继续使用客户端自报的权威 `cost_usd`,**不重算、不覆盖**。

### 关键区别:估算 vs 权威

- Claude 的 `cost_usd` 是客户端**自报**的权威值(本项目从不计算,只透传)。
- Codex/第三方的 `cost_usd` 是本项目**基于计价表算出的估值**。
- 二者语义不同,dashboard 需在 codex/all 视图明确标注「含估算」,不得静默混同(见 §7)。

---

## 2. 已拍板决策

| 决策 | 选择 | 理由 |
|---|---|---|
| 作用范围 | **通用估算引擎**:按 `(model, token 数)` 算 cost 的客户端无关能力。Codex 先用;未来任何不自报 cost 的第三方模型复用同一套。Claude 仍用自报 cost,不覆盖 | 一次投入,覆盖 Codex 与后续第三方模型 |
| 计价来源 | **LiteLLM 格式 JSON**,位置由 config 指定(**不 vendor 进仓库**);本地文件为必填基线,URL 为可选刷新源;另有 config 级 per-model override 叠加在最上层 | LiteLLM JSON 字段(input/output/cached/reasoning 单价)与所需口径完全对齐,社区维护、无需 auth |
| 计算时机 | **ingest 时**计算,materialize 为 `codex_event_token_usage` 的新列 `cost_usd` | 查询最简单、BI/duckdb CLI 直接可见;代价是单价在写入时冻结(见 §12) |
| 成本展现 | **跟随现有 client 筛选器**:`all`=两家相加、`claude`=权威、`codex`=估算;codex/all 视图标注「含估算」 | 复用阶段二已有的 client 筛选机制,UI 改动最小 |
| 启动策略 | **本地文件强制兜底**:`enabled=true` 时 `source_file` 必填且启动即加载,不可解析则 **fail-fast**;URL 拉取成功后**热替换** | 本地文件保证启动即有可用计价表,不会因远程源未就绪而产生永久 NULL 行 |
| 默认开关 | `pricing.enabled` **默认 false** | 现有部署零影响,行为完全不变 |

---

## 3. 计价模块 `internal/pricing`(通用引擎)

### 3.1 职责与接口

模块**只认识「模型名 + token 数」**,不认识 Codex/Claude。对外暴露一个纯函数式查表接口:

```go
type TokenCounts struct {
    Input     int64
    Output    int64
    Cached    int64  // OpenAI 口径:cached ⊂ input
    Reasoning int64  // OpenAI 口径:reasoning ⊂ output
    Tool      int64  // 保留但不参与计价(见 §3.3);仅为完整性携带
}

type Engine interface {
    // 未匹配到计价 / 引擎未启用 / 缺 input|output 单价 → 返回 sql.NullFloat64{Valid:false}
    CostFor(model string, c TokenCounts) sql.NullFloat64
}
```

内部持有 `map[string]ModelPrice` 计价表(读多写少,刷新时整表原子替换,读侧无锁竞争)。`ModelPrice` 只取所需字段,**用指针区分「字段缺失」与「显式为 0」**(其余 LiteLLM 字段忽略):

```go
type ModelPrice struct {
    InputCostPerToken           *float64  // input_cost_per_token(必需,缺失则该 model 不可计价)
    OutputCostPerToken          *float64  // output_cost_per_token(必需)
    CacheReadInputTokenCost     *float64  // cache_read_input_token_cost(可选;nil=缺失,0=显式免费)
    OutputCostPerReasoningToken *float64  // output_cost_per_reasoning_token(可选;多数 o 系列缺失)
}
```

> 已核实(LiteLLM `model_prices_and_context_window.json` main):顶层是以 model 名为 key 的对象(非数组),含一个占位用的 `sample_spec` 顶层条目(解析时跳过);上述四个字段名逐字存在;取值为「每单 token 美元」的极小浮点(如 `gpt-4o` 的 `input_cost_per_token=2.5e-6`、`cache_read_input_token_cost=1.25e-6`);`output_cost_per_reasoning_token` 在多数 OpenAI o 系列条目上缺失(reasoning 按 output 单价计)。另有 `*_cost_per_1k_*` 等独立字段与本引擎无关,不解析。

### 3.2 计价表来源(三层叠加,不是互斥替换)

按 key 分层叠加,**上层覆盖下层的同名 key,下层独有的 key 保留**(precedence:overrides > URL > file):

1. **本地文件**(必填基线):启动即加载,构成 base 层。
2. **URL 拉取的 LiteLLM JSON**(可选):启动时拉一次 + 每 `refresh_interval` 拉一次;成功则以 URL 数据**按 key merge over file 层**(URL 有的 key 覆盖 file 同名 key,**file 独有的 key 不丢失**——保证运维在本地文件里补的自托管型号不会被一次远程刷新抹掉)。
3. **config `overrides`**:手写单价,叠加在最上层,同名 key 覆盖 file/URL。

内部实现:base = file;有 URL 则 `mergeOver(base, url)`;最后 `mergeOver(result, overrides)`。刷新只重算前两层再叠 overrides,整表原子替换。

### 3.3 计价公式(处理 OpenAI 子集口径,防重复计价)

```
若 InputCostPerToken == nil 或 OutputCostPerToken == nil → 返回 NULL(不可计价)

input_rate  = *InputCostPerToken
output_rate = *OutputCostPerToken
cached_rate = (CacheReadInputTokenCost != nil) ? *CacheReadInputTokenCost : input_rate
             # 仅字段「缺失(nil)」时回退 input_rate;显式为 0 则按 0 计(cache 免费)

cost = max(0, input - cached) * input_rate
     + cached                 * cached_rate

若 OutputCostPerReasoningToken != nil:
    cost += max(0, output - reasoning) * output_rate
          + reasoning                  * (*OutputCostPerReasoningToken)
否则:
    cost += output * output_rate       # output 已含 reasoning,统一按 output 单价
```

- `tool_token_count` **不计费**(视为已含在 input/output 内,与阶段一「总量 = input + output」口径一致)。`TokenCounts.Tool` 字段保留但公式不引用。**实现第一步须用阶段一已有的真实 protobuf golden 核对该子集假设**;若 golden 证明 tool 不是 input/output 子集,则**属于本 spec 需重新评审的改动**(需引入 tool 单价字段与计费项),不在当前实现范围内擅自处理。
- `input - cached`、`output - reasoning` 为负时钳到 0(防脏数据)。

### 3.4 model 名匹配顺序

对原始 `model` 值,依次:

1. `overrides` 精确匹配
2. LiteLLM 表(file/URL merge 后)精确匹配
3. **归一化**后再查:先剥 `provider/` 前缀(如 `openai/gpt-4o` → `gpt-4o`),再剥结尾日期快照后缀(正则 `-\d{4}-\d{2}-\d{2}$`,如 `gpt-4o-2024-08-06` → `gpt-4o`);归一化后的 key **仍按同顺序**查(先 overrides,再 LiteLLM 表)
4. 仍未命中 → **返回 NULL**

匹配为**只读查表**,与 `internal/dashboard/classifier.go` 的展示分组是两套逻辑(classifier 管归组显示,pricing 管取价),互不复用,避免耦合。

### 3.5 未匹配处理

- 返回 `NULL`(**非 0**):区分「未知价格」与「免费」。
- 累加 `unmatched[model]` 计数器 + 去重 Debug 日志(每个 model 名只记一次),供运维发现缺价并补 override(见 §8)。

---

## 4. 配置(`internal/config`)

顶层新增 `pricing:` 段,**默认关闭**:

```yaml
pricing:
  enabled: false                       # 默认关,现有部署零影响
  source_file: ./pricing/litellm.json  # enabled 时必填;本地基线,仓库不带此文件
  source_url: "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"  # 可选刷新源,留空则只用本地文件
  refresh_interval: 24h                # 复用现有自定义 Duration 类型;source_url 为空时忽略
  overrides:                           # 可选;叠加在最上层
    gpt-5-codex:
      input_cost_per_token: 0.00000125
      output_cost_per_token: 0.00001
      cache_read_input_token_cost: 0.000000125
```

`Config` 新增 `Pricing PricingConfig`(参照现有 sub-config 写法);`PricingConfig` 用现有自定义 `Duration` 类型承载 `refresh_interval`,用类似 `ModelGroupRule` 的方式承载 `overrides`。

- `applyDefaults`:`refresh_interval` 缺省补 24h。
- `validate`:`enabled=true` 时 `source_file` 非空,且启动时能成功打开并解析,否则 **fail-fast**(启动期错误,日志 + 非零退出);`source_url` 为空时不启动刷新 goroutine;`overrides` 单价为负 → 报错。
- `config.example.yaml` / `config.dev.yaml` / `config.docker.yaml` 增补注释掉的 `pricing:` 示例段。

**dashboard 也需感知 `pricing.enabled`**:该布尔值注入 dashboard 层用于门控展示(见 §7.2/§7.3),不再单独引入运行时探测。

---

## 5. 数据模型与迁移

新增迁移 `internal/store/migrations/004_codex_token_usage_cost.sql`:

```sql
ALTER TABLE codex_event_token_usage ADD COLUMN cost_usd DOUBLE;
```

- 版本化迁移,单次应用由 `schema_migrations` 保证(沿用 `002/003` 机制)。
- 已核实:`codex_event_token_usage` DDL 最后一列是 `attrs`(`003_codex_event_tables.sql`),`ADD COLUMN` 使 `cost_usd` 成为新的最末列(排在 `attrs` 之后)。
- `cost_usd` 的三种 NULL 含义(引擎未启用 / 未匹配 / 语义上无价)在列层面不区分,均为 NULL;运行时由 §8 的 stats 区分。**不新增 `pricing_source` / `pricing_version` 等溯源列**(保持最小改动)。

`CodexEventTokenUsageRow`(`internal/otlp/codex_rows.go`)新增字段:

```go
CostUsd sql.NullFloat64
```

> 已核实:入库列序由 **mapper**(`mapCodexTokenUsage`)决定,**不由 struct 字段顺序决定**(Appender 消费 mapper 产出的 `[]driver.Value`)。该 struct 头部嵌入 `CodexCommonAttrs`(其中含 `Attrs`),末尾字段是 `DurationMs`;`Attrs` 并非本 struct 的末尾字段。因此 `CostUsd` 放在 struct 何处不影响持久化,约定追加在 `DurationMs` 之后即可;**真正 load-bearing 的是 §6 里 mapper 的追加顺序**。

---

## 6. Ingest 集成

数据流沿用现有架构,只在 Codex token-usage 分支插入一次 enrich:

| 位置 | 改动 |
|---|---|
| `internal/pricing/`(新增) | 计价引擎(§3):加载器(file + url + overrides 三层叠加)、匹配、公式、后台刷新 goroutine、stats 快照 |
| `internal/otlp/codex_rows.go` | `CodexEventTokenUsageRow` 追加 `CostUsd sql.NullFloat64`(约定放 `DurationMs` 之后) |
| `internal/otlp/dispatch.go` | `dispatchCodexEvent` 的 `codex.sse_event`(`response.completed`)分支:`parseCodexTokenUsage` 返回**值** `r`(非指针),在 `r, err := parseCodexTokenUsage(...)` 的 err 检查之后、`return d.sink.AppendEvent(r)` **之前**插入:`r.CostUsd = engine.CostFor(r.Model.String, pricing.TokenCounts{...})`。`r.Model` 是 `sql.NullString`(取 `.String`);因返回值故直接改本地副本即可,无需指针。**parser 保持纯净**(只做协议→struct,不掺计价) |
| `internal/store/codex_mappers.go` | `mapCodexTokenUsage` 在 append 列表**末尾、`attrs` 之后**追加 `nullFloat64(r.CostUsd)`,与新 DDL 列序对齐。若 `appender.go` 无 `nullFloat64` 辅助函数,则仿 `nullInt64/nullStr` 新增一个 |
| `cmd/server/` | wire-up:构造 `pricing.Engine` 注入 OTLP 层(dispatcher);启动后台刷新 goroutine;注册优雅关闭(停刷新 goroutine)。同时把 `pricing.enabled` 传给 dashboard 层(§7) |

**关键不变量**:
- parse 路径只做**内存 map 查表**,零外部调用、零阻塞;URL 拉取全在后台 goroutine,永不阻塞 ingest。
- `enabled=false` 时引擎为 no-op(`CostFor` 恒返回 `NullFloat64{Valid:false}`),`cost_usd` 恒 NULL,链路行为与现状完全一致。
- 引擎注入而非包级全局(遵循「避免包级可变全局」)。

---

## 7. Dashboard 集成

### 7.1 成本查询(`internal/dashboard/queries.go`)

`QueryPeriodCost` / `QueryCostSparkline` / `QueryModelCost` 现为 Claude-only(`FROM metric_cost_usage`,codex 分支直接返 0/nil)。改为按 `client` 参数增加 codex arm,**沿用现有查询「Go 层累加两 arm」的模式**(period/sparkline/model 类查询目前就是分别查 claude arm 与 codex arm 后在 Go 里合并,而非 SQL UNION;仅 `QuerySessionList` 用 SQL `UNION ALL`):

| client | 成本来源 |
|---|---|
| `claude` | `SUM(value)` FROM `metric_cost_usage`(现状) |
| `codex` | `COALESCE(SUM(cost_usd), 0)` FROM `codex_event_token_usage`(NULL 行不计入;**SQL 侧 COALESCE 防止全 NULL 时返回 NULL**) |
| `all` | 两 arm 各自 `COALESCE` 后在 Go 层相加 |

时区 / 时间窗处理沿用现状。

**API 响应新增门控标志**:overview 响应体增加 `cost_estimated bool`(= `pricing.enabled`),供前端判断是否展示 codex 估算成本(handler 从注入的配置读取后透传)。

### 7.2 前端

- 成本 KPI 卡与模型明细的成本列:门控条件从现有的 `client !== 'codex'` 改为 `client !== 'codex' || cost_estimated`——即**仅当 `pricing.enabled=true` 时**才在 codex 视图展示成本卡/成本列;`pricing.enabled=false`(默认)时保持阶段二的隐藏行为,**不出现空的「含估算」卡**。
- 展示时,codex/all 视图在成本卡上标注「含估算」(角标 / tooltip)。
- 模型明细成本列在 codex/all 视图展示;未匹配到价的第三方模型其成本以 0 计入(显示 `$0.00`),运维据 §8 的 unmatched 列表补 override。(在列层面区分「0」与「未匹配」需给 `ModelBlock.Cost` 加可空/标志位,成本高于收益,本期不做。)

### 7.3 热点图(`internal/dashboard/heatmap.go`)

阶段二因 Codex cost 恒零,给 `client=codex` 视图从强度分母**剔除了 cost 权重**(`wsum = wT + wR`),否则恒零的 cost 会系统性压低每个 codex-only 分数。

本期**有条件恢复三权重**:`BuildHeatmap` 感知 `pricing.enabled`——

- `pricing.enabled=true` 时,`client=codex` 恢复三权重(`score = (wT·nT + wR·nR + wC·nC)/(wT+wR+wC)`),与 all/claude 一致;
- `pricing.enabled=false`(默认)时,`client=codex` **保持阶段二的两权重**(`wsum = wT + wR`),避免恒 NULL 的 cost 再次压低分数。

> 说明:即便启用后仍有部分模型未匹配到价(§3.5 → NULL),其 cost 项为 0,会轻微拉低这些天的分数;这是可接受的(有价数据越全越准),不再额外门控到「逐模型是否有价」粒度。

### 7.4 会话页(与阶段二决策的一致性)

阶段二会话列表/详情对所有客户端都不显示 cost。为与本期「dashboard 展示两家成本」保持一致,会话页也补齐成本,**两家都显示**:

- **Claude(权威)**:列表与详情都展示 Claude 会话成本(`SUM(metric_cost_usage.value)`,按 `session_id` 聚合),恒展示(本就是权威数据,此前只是没露出);无「含估算」标注。
- **Codex(估算,门控)**:列表 / 详情成本 = `COALESCE(SUM(cost_usd),0)`(按 `conversation_id` 聚合),仅 `pricing.enabled=true` 时展示并标注「含估算」;`pricing.enabled=false` 时该会话成本置空不展示。
- 具体落点:`QuerySessionList` 增加成本 CASE arm(claude→metric_cost_usage、codex→cost_usd),`sessionListRow` / `SessionSummary` 加可空 `cost` + `cost_estimated`;`buildClaudeSessionDetail` / `buildCodexSessionDetail` 各自填 `SessionDetailResponse.Cost` / `CostEstimated`。

> 这是本设计相对阶段二的**新增面**,已确认纳入本期(与聚合 dashboard 保持一致——两家都展示成本,避免"dashboard 有成本、会话页无"的不一致)。

---

## 8. 可观测性

复用 `internal/stats` 自监控端点,暴露计价引擎状态:

- `pricing_enabled`、`pricing_entries`(表条目数)
- `pricing_last_refresh_at`、`pricing_last_refresh_source`(`file` / `url`)、`pricing_last_refresh_ok`
- `pricing_unmatched_models`:未匹配到价的 model 名 Top-N + 各自出现次数

让运维一眼看出:计价表是否加载、是否刷新成功、哪些型号漏价该补 override。

---

## 9. 错误处理与启动/停用策略

- **启动**:`enabled=true` → 同步加载 `source_file`;失败(文件缺失 / 解析错)→ **fail-fast**(启动期错误,日志 + 非零退出)。
- **URL 刷新**:后台 goroutine;成功 → 按 §3.2 merge over 后原子热替换 + 重叠 overrides;失败 → **保留上一次好的表** + warn 日志 + 下个周期重试(绝不因刷新失败清空表)。
- **单行计价**:`CostFor` 内部不 panic;任何异常(如脏 token 数)→ 返回 NULL,不中断整批 ingest。
- **未识别 model**:NULL + unmatched 计数(§3.5),不报错。
- **启用后又停用**(`enabled` 从 true 改回 false):历史已计价的行**保留其 `cost_usd`**(§12 不回填、不清除);但所有前端/热点图/会话页展示均门控在**当前** `pricing.enabled` 上(§7.2/§7.3/§7.4),故停用后这些估算 cost 立即从 UI 消失,不会残留「半截估算」。聚合查询在停用时因门控不再展示 codex 成本(数值层面历史行仍在库,可经 BI 直接查到,属预期)。

---

## 10. 测试策略

- `internal/pricing` 单测:
  - 子集口径公式(cached/reasoning 不重复计;`tool` 不计费)
  - 匹配顺序(override > 精确 > 归一化 > NULL);归一化剥 provider 前缀与日期后缀
  - `cache_read` **缺失(nil)**回退 input 单价;`cache_read` **显式 0** 按 0 计(两用例都要有,验证 nil vs 0 区分)
  - 缺 input 或 output 单价 → NULL
  - `input - cached` / `output - reasoning` 负数钳零
  - 三层叠加:overrides 覆盖同名 key;URL merge over file 后 file 独有 key 存活
  - URL 刷新失败保留旧表
- ingest golden(`internal/otlp`):
  - `enabled=true` 下 `codex_event_token_usage` 行带出正确 `cost_usd`
  - `enabled=false` 下 `cost_usd` 恒 NULL
  - 未匹配 model → `cost_usd` NULL
- dashboard 单测(`internal/dashboard`,内存 DuckDB + 种子数据):
  - 三态 client 成本卡数值(all=两家和、claude=权威、codex=估算);codex arm 全 NULL 时 all 不被 NULL 污染(COALESCE 生效)
  - `cost_estimated` 标志随 `pricing.enabled` 正确出现在响应
  - 热点图:`pricing.enabled=false` 时 codex 两权重、`=true` 时三权重
  - 会话(若纳入 §7.4):列表/详情成本随 `pricing.enabled` 正确门控
- `go test -race` 全绿。

---

## 11. 风险与缓解

| 风险 | 缓解 |
|---|---|
| LiteLLM 模型命名与 Codex `model` 值对不上(如 `gpt-5-codex` 未收录) | 三层匹配 + 归一化;缺价走 override;§8 unmatched 可观测,运维按需补 |
| `tool_token_count` 是否为 input/output 子集不确定,误计会重复计价 | 实现第一步用阶段一已有真实 protobuf golden 核对;默认不计费(偏保守);若假设不成立则回本 spec 评审(§3.3) |
| LiteLLM JSON schema 漂移(字段增删) | 只取所需 4 个字段(指针可选),其余忽略;字段缺失有回退(cached→input);解析容错不 fail 整表 |
| ingest 时冻结单价:调价 / 计价 bug 修复不回溯历史行 | 已接受的代价(§12);后续可加离线回填 job(本期非目标) |
| overrides 单价写错(量级错误,如漏写几个 0) | validate 校验非负;§8 可观测 + 文档给正确量级示例 |
| 默认禁用态下误展示空成本 | 前端/热点图/会话页均门控在 `pricing.enabled`(§7.2/§7.3/§7.4/§9) |

---

## 12. 非目标(本期不做)

- **不重算 Claude 的 cost**(它自报,权威)。
- **不做查询时动态重定价**(计算时机选了 ingest 时冻结)。
- **不做历史行回填 / 重定价 job**(可作为后续独立任务)。
- **不给 `codex_event_api_request` 加 cost**(它无 token)。
- **不 vendor LiteLLM JSON 进仓库 source**;但 **release 压缩包与 docker 镜像在 CI 构建时下载并内置一份** `litellm.json` 作为开箱基线(release 包 `pricing/litellm.json`;镜像 `/etc/claude-code-monitor/pricing/litellm.json`),见 `.github/workflows/release.yml` 与 `Dockerfile`。仓库 git tree 仍不含该文件。
- **不在其他表(未来第三方客户端)上落 cost 列**:引擎已客户端无关,届时按 §5/§6 机械复制即可,本期只接 `codex_event_token_usage`。
- **不新增溯源列**(`pricing_source` / `pricing_version` 等)。
