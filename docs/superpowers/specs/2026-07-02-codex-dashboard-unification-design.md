# 设计:dashboard 查询层统一(Codex 阶段二)

- 日期:2026-07-02
- 状态:已评审通过,待实现
- 前置:阶段一(v2.2.0)已完成 Codex 遥测落库,见 `2026-07-01-codex-otel-support-design.md`

---

## 1. 背景与目标

v2.2.0 起 Codex 遥测已落入 6 张 `codex_event_*` 表,但 dashboard 的 6 个查询端点与 Web UI 仍只查 Claude 表族,Codex 数据只能用 duckdb CLI 手查。本期目标:**Web UI 统一展示两家客户端的用量**,支持按 client 筛选。

## 2. 已拍板决策

| 决策 | 选择 |
|---|---|
| UI 呈现 | **统一视图 + 全局 client 筛选器**(全部 / Claude / Codex,默认全部);不做独立页签 |
| 会话融合 | **完全融入**:Codex conversation 混入会话列表(带 client 徽标),详情页复用现有布局并隐藏成本 / skill 卡片 |
| 成本 | 恒 Claude-only(Codex 无 cost 数据);client=codex 时前端隐藏成本卡片 |
| 请求数口径 | Codex 以 `codex_event_token_usage` 行数计(= `response.completed` 条数),不用 attempt 粒度的 `codex_event_api_request` |
| token 口径 | Codex 总量 = `input_token_count + output_token_count`(cached ⊂ input、reasoning ⊂ output,**不得再加**);Claude 总量 = input+output+cacheRead+cacheCreation。两侧各自算好再合并 |

## 3. 交互模型(前端)

- `App.tsx` 顶部新增 client 筛选器,复刻现有 range-toggle 样式(`App.tsx:301-313`);state `client: 'all'|'claude'|'codex'`,默认 `all`,进 `useEffect` 依赖数组
- 仪表盘(KPI 四卡 / 趋势图 / 热点图 / 模型明细 / 排名)与会话列表全部跟随筛选
- `client=codex` 时:成本 KPI 卡与模型表中的成本列隐藏;**排名区(工具 + skill 甜甜圈)整体隐藏**——rankings 端点本期维持 Claude-only(见 §10),`client=all` / `claude` 时照常展示并在标题旁标注「仅 Claude」
- `api/dashboard.ts` 的 `Dashboard.fetch(range, since, client)` 将 client 拼入全部端点 URL;`api/sessions.ts` 同理

## 4. API 变更

- 6 个端点全部接受 `client=all|claude|codex`,缺省 `all`,非法值 400(与现有 `range` 校验风格一致,`handler.go` 各 handler 解析后透传 `Build*`)
- 响应结构:数值随筛选变化,形状基本不变;`SessionSummary` 与 `SessionDetailResponse` 新增 `client` 字段(`"claude"` / `"codex"`)
- 会话详情 `GET /api/sessions/{id}?client=`:前端从列表行回传 client;未携带时后端先按 Claude 查,无结果再按 Codex 查

## 5. 查询层口径(internal/dashboard/queries.go)

| 指标 | Claude 源 | Codex 源 | 合并规则 |
|---|---|---|---|
| Token 总量 | `SUM(value)` FROM `metric_token_usage`(全类型) | `SUM(input_token_count + output_token_count)` FROM `codex_event_token_usage` | 各自聚合后相加 |
| Token 分类(input/output/cache) | 按 `type` 拆 | input=`input_token_count`,output=`output_token_count`,cacheRead≈`cached_token_count` | 快照卡片分项对应累加;Codex 的 reasoning/tool 计数不进合并分项,仅详情页展示 |
| 请求数 | `COUNT(*)` FROM `event_api_request` | `COUNT(*)` FROM `codex_event_token_usage` | 相加 |
| 成本 | `SUM(value)` FROM `metric_cost_usage` | 无 | client=codex 时查询返回 0,前端隐藏 |
| 缓存命中率 | cacheRead / 输入侧 | `SUM(cached_token_count)` / `SUM(input_token_count)` | 分子、分母各自累加后再除(两家语义均为「输入中缓存命中的占比」) |
| 模型明细 / 趋势 | 现有查询 | UNION `codex_event_token_usage` 的 `model` + token(同口径) | `gpt-5.5` 走现有 Classifier 兜底 → 前端「第三方模型」;`config.example.yaml` 增加注释掉的 `model_groups` 正则示例(如 `^gpt-(\d+)` → `gpt-$1`) |

实现方式:涉及 token / requests / model 的查询按 client 参数生成——`claude` / `codex` 只查一侧,`all` 用 UNION ALL 子查询(每侧先按自家口径投影成统一列)再聚合。时区与时间窗处理沿用现状(Go 层算 UTC instant,SQL 桶用 `ts + INTERVAL`)。

## 6. 热点图(heatmap.go + 三个 sparkline 查询)

- `QueryTokensSparkline` / `QueryRequestsSparkline` 加 client 参数并 UNION Codex 源(口径同 §5);`QueryCostSparkline` 恒 Claude-only
- **强度权重分母按 client 动态调整**:`client=codex` 时从分母剔除 cost 权重(`score = (wT·nT + wR·nR)/(wT+wR)`),避免恒零的 cost 把 Codex 视图分数系统性压低;`all` / `claude` 保持现状三权重

## 7. 会话页

### 7.1 列表(sessions.go / queries.go 活动并集)

- 活动时间 UNION ALL 扩展到 Codex 四表:`codex_event_token_usage` / `codex_event_user_prompt` / `codex_event_tool_result` / `codex_event_conversation_starts`,统一 `conversation_id AS session_id`,并投影常量列 `client`
- 每行指标:token(各家口径)、请求数(同 §5)、tool_calls(Codex 用 `codex_event_tool_result` 计数)、skill_activations(Codex 恒 0)、cost(Codex 恒 NULL)
- 前端 `SessionsView` 行上加 Claude / Codex 徽标;列表跟随全局 client 筛选

### 7.2 详情(SessionDetailView)

- Codex 会话复用现有布局:**隐藏**成本卡与 skill 饼图(不渲染,而非显示 0);工具饼图用 `codex_event_tool_result.tool_name` 聚合
- token 分项卡展示 Codex 特有四维:input / output / cached / reasoning(reasoning 仅 Codex 有,Claude 会话不显示该维)
- 模型分布:`codex_event_token_usage` 按 model 聚合

## 8. 错误处理

- `client` 参数非法 → 400(复用现有参数校验风格)
- 会话详情按两族表都查不到 → 404(现有行为不变)
- Codex 表为空(未部署 / 未产生数据)时,`all` 视图退化为 Claude-only 数值,无错误

## 9. 测试

- 沿用 `internal/dashboard` 现有测试模式(内存 DuckDB + 种子数据):
  - 种子数据扩展出 Codex 行(codex_event_token_usage / tool_result / user_prompt / conversation_starts)
  - client 三态用例:`all` = 两家之和、`claude` / `codex` = 单侧
  - **口径断言**:Codex token 总量不含 cached / reasoning(防重复计);请求数取 token_usage 行数而非 api_request 行数
  - 热点图:client=codex 时分数分母不含 cost 权重
  - 会话:列表包含两家、client 字段正确;详情按 client 查询正确的表族
- 前端无自动化测试基建,不新增;以 `npm run build` 通过 + 手工验收为准

## 10. 非目标

- Codex 成本估算
- Codex 独立页签
- 事件级会话时间线(两家都无 event_sequence 可靠序列)
- rankings 端点的 Codex 工具排名合并(工具命名空间不同:`exec_command` vs `Bash`;本期排名维持 Claude-only,Codex 工具仅在会话详情页展示)
