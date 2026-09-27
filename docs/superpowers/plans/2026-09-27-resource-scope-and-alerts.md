# 节点分组权限边界修复 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 受限用户跨节点读取只能看到授权节点的指标，并堵住别名与标签参数绕过，给测试告警加权限，补前端回归测试与同步文档。

**Architecture:** 统一在 API 层解析有效节点目标、拒绝矛盾参数；对受限身份的跨节点序列进行节点分组裁剪，空授权集合不调用 TSDB；业务路由使用现有 `permit` / 节点包装器，存储层接口不变。跨节点视图继续可用，全局用户和关闭登录认证保持兼容。

**Tech Stack:** Go 1.25、net/http、`storage.Storage`、Vue 3、Vitest 2、`@vue/test-utils`。

**Spec:** `docs/superpowers/specs/2026-09-27-scope-and-server-reliability-design.md`（本计划仅覆盖权限、告警、前端和文档；HTTP 生命周期单独成计划）。

## Global Constraints

- 不新增身份系统、不变更 `storage.Storage` 接口、不新增产品依赖；保持全局用户与未开启认证时的现有跨节点行为。
- 受限用户的未知 / 无归属节点与无 `node` 标签序列不可见；空范围绝不退化为全量，显式范围外拒绝写授权审计。
- 指标浏览与仪表盘在受限范围内继续支持跨节点查询；权限安全边界始终在服务端。
- 当前在 `main` 分支，工作区已有未跟踪的 `CLAUDE.md`、`docs/agents/`、`_backup/` 和专利文档；不要覆盖或暂存它们。未收到提交授权，不执行本技能模板中的 commit 步骤。

## Review Focus

1. `?node=web-01&labels.node=db-01` 与反向组合必须 400，不能用标签覆盖查询目标；见任务 1 测试。
2. 受限用户请求未知节点或 `ScopeRestricted` 空 `Groups` 时不得因未知 / 空集放大授权；见任务 1、2 测试。
3. TSDB 在指定节点后仍返回错标节点或无 `node` 标签时响应也必须裁剪；见任务 2 测试。
4. `?hostname=db-01&node=web-01` 等参数矛盾不能以授权和 handler 选择优先级不同绕过；见任务 3 测试。
5. 无 `notify:write` 用户不能发送测试告警，有权限用户及关闭认证部署不能被误阻止；见任务 4 测试。

---

## File Structure

- `internal/server/api/metric_scope.go`：指标查询参数统一解析、有效节点归属校验、可见节点与序列过滤，不调用具体 TSDB。
- `internal/server/api/metric_scope_test.go`：真实路由表 + 可配置的 `storage.Storage` 假实现覆盖指标查询、导出、活跃状态授权。
- `internal/server/api/auth.go` + `scope_test.go`：其它节点接口的 `node` / `hostname` 参数一致性、范围校验。
- `internal/server/api/query.go`、`export.go`、`catalog.go`：只调用指标范围函数，保留现有查询、导出结构与验证。
- `internal/server/api/scope_ops_test.go`：测试告警路由权限回归；`web/src/components/AlertsView.vue` + `web/src/components/AlertsView.test.js`：按钮可见性。
- `web/src/components/metrics/MetricsExploreView.test.js`：跨节点请求与拒绝反馈回归；`README.md`、`CONTEXT.md`：只更新已确认过时的描述。

### Task 1: 统一指标节点参数解析与可见范围

**Files:** Create `internal/server/api/metric_scope.go`, `internal/server/api/metric_scope_test.go`; Modify `internal/server/api/query.go:175-177,359-362`。

**Interfaces:**
- Produces: `func (a *API) metricTarget(w http.ResponseWriter, r *http.Request, perm string, labels map[string]string) (string, bool)`：从 `node`、`labels.node` 求有效节点，移除 `labels.node` 防止重复选择器；权限拒绝或冲突时已写响应并返回 `false`。
- Produces: `func (a *API) visibleMetricNodes(p *auth.Principal) bool`：全局/无认证返回 true；受限身份需要至少一个所属分组可见节点。
- Produces: `func (a *API) visibleMetricSeries(p *auth.Principal, node string, series []model.Series) []model.Series`：仅受限用户裁剪，按结果的 `Labels["node"]` 及可选明确 `node` 比对，不改变全局/无认证序列。
- Consumes: `Principal(r)`、`a.nodeMgr.GetNode` / `ListNodes`、`a.denyScope`、`a.nodeInScope`；后续任务 2 调用以上函数。

- [ ] **Step 1: 写失败测试**：在 `metric_scope_test.go` 构建真实路由与假存储（实现 `storage.Storage`，分别可记录 `QueryRange` / `QueryInstant` 的调用及请求标签，返回 `web-01`、`db-01`、无节点标签的样本）。测试 `GET /api/v1/query/range?node=web-01&labels.node=db-01&metric=cpu_usage&start=1000&end=61000&step=1000` 返回 400 且没有存储调用；反向组合也为 400；`?labels.node=db-01` 返回 403 且审计拒绝；`labels.node=web-01` 生效且传给存储的 `node` 为 web-01，labels 不重复包含 node；未知节点（如 ghost）对受限用户拒绝；全局用户允许未知节点但参数冲突仍拒绝；空资源范围请求无节点返回 `series:[]` 且零次查询。`QueryLatest` 的重复标签冲突也必须 400。测试构造参照 `scope_test.go:19-83`，结果断言参照 `scope_test.go:114-125`。

```go
// 测试核心断言：结果码、审计、存储调用都需要验证，而非仅断言 403。
rec := httptest.NewRecorder()
newRoutesMux(a).ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"nodes:read"}, "g1"), http.MethodGet, "/api/v1/query/range?node=web-01&labels.node=db-01&metric=cpu_usage&start=1000&end=61000&step=1000", ""))
if rec.Code != http.StatusBadRequest || store.rangeCalls != 0 { t.Fatalf("冲突节点不应查询 TSDB: %d, %d", rec.Code, store.rangeCalls) }
```

- [ ] **Step 2: 运行失败测试**：`go test ./internal/server/api -run 'TestMetricScope_' -count=1`；预期至少冲突 400 / 空范围零查询等断言失败。
- [ ] **Step 3: 实现最小解析和过滤辅助函数**：`metricTarget` 对 `q["node"]` 的重复值与 `labels.node` 的存在性作校验：若标签存在却为空，或两个非空节点不同，响应 400；只有标签节点时也按明确节点校验权限；`delete(labels, "node")` 消除重复 matcher。受限未知节点走 `denyScope`（group 不可归属可传空串，不泄露节点归属）。`visibleMetricNodes` 遍历 `ListNodes` 用 `p.CanAccessGroup`；`visibleMetricSeries` 用 `a.nodeInScope` 且需要标签非空，若显式 node 非空则强制相等。若 `node` 与标签各自存在但重复且不一致同样 400。为范围路由改用 `a.permit(...)`（让 helper 统一处理参数授权），`query/latest` 同理；活跃和导出在任务 2 接入。

```go
func (a *API) visibleMetricSeries(p *auth.Principal, node string, series []model.Series) []model.Series {
    if p == nil || p.Scope.IsGlobal() { return series }
    out := make([]model.Series, 0, len(series))
    for _, s := range series {
        name := s.Labels["node"]
        if name != "" && (node == "" || node == name) && a.nodeInScope(p, name) { out = append(out, s) }
    }
    return out
}
```

- [ ] **Step 4: 在 `handleQueryRange` / `handleQueryLatest` 中接入 helper**：范围请求解析原始 labels 后，调用 `metricTarget`；空授权节点时返回 `{"series": []model.Series{}}` 而非查询。查询返回后先调用 `visibleMetricSeries`，再组成结果；latest 在选择 `point` **之前**过滤。`query/latest` 继续要求有效节点非空；避免从未经裁剪的第一条计算点。
- [ ] **Step 5: 运行测试并检查无回归**：`go test ./internal/server/api -run 'TestMetricScope_|TestRoutes_QueryWithNodeParam' -count=1`，预期 PASS；`go test ./internal/server/api -count=1`，预期 PASS。

### Task 2: 导出、活跃状态与节点列表调用同一范围规则

**Files:** Modify `internal/server/api/export.go:20-96`, `internal/server/api/catalog.go:26-55`, `internal/server/api/query.go:359-362,633-642`; Test `internal/server/api/metric_scope_test.go`。

**Interfaces:** Consumes `metricTarget`、`visibleMetricNodes`、`visibleMetricSeries`（任务 1）；不改变 `storage.Storage` 签名和 JSON / CSV 字段契约。

- [ ] **Step 1: 写失败测试**：为受限 `g1` 用户添加导出 `?metric=cpu_usage&start=1000&end=61000&step=1000` 用两组返回序列，断言 CSV 仅含 web-01 数据、使用单序列头 `timestamp,value`；带 `node=web-01&labels=node=db-01` 返回 400 不调用存储；`labels=node=db-01` 返回 403 且留审计；`ScopeRestricted` 空组导出只有标题行且不查存储。`metrics/active?category=...` 假 `QueryInstant` 仅有 db-01、无节点标签时相应指标应 inactive；web-01 有数据则 active；空授权组零次调用；全局用户不改变 active 语义。明确节点返回别的节点序列时也应 inactive。测试使用 `metrics.List()` 取真实目录中的首个 Name/Category，避免硬编码错误分类。`GET /api/v1/nodes/latest` 假存储返回 `web-01`、`db-01` 和未注册 `ghost` 三个节点的 `cpu_usage`，受限 g1 用户仅得到 web-01；空组得到空指标表；全局用户维持原有可见结果。

```go
rec := httptest.NewRecorder()
newRoutesMux(a).ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"metrics:export"}, "g1"), http.MethodGet, "/api/v1/metrics/export?metric=cpu_usage&start=1000&end=61000&step=1000", ""))
if strings.Contains(rec.Body.String(), "db-01") || !strings.Contains(rec.Body.String(), "timestamp,value") { t.Fatalf("导出越过资源范围: %s", rec.Body.String()) }
```

- [ ] **Step 2: 运行新测试确认失败**：`go test ./internal/server/api -run 'TestMetricExportScope_|TestMetricActiveScope_' -count=1`；预期跨节点结果或冲突参数测试失败。
- [ ] **Step 3: 更正导出参数合并**：`parseInt64`、时间上限等保持原样；解析 `labels` 时不让 `labels.node` 直接覆盖 `node`，而是交给 `metricTarget` 判冲突并规范化；用返回的有效 node 调用 `QueryRange`；受限空节点集返回空切片，查询后先 `visibleMetricSeries` 再决定 CSV 表头。附加 `instance` 标签行为不变；`labelStr` 保持原有导出显示格式。
- [ ] **Step 4: 更正活跃状态与节点列表**：保留目录循环，开头通过 `metricTarget(w,r,"nodes:read", map[string]string{})` 规范 `node`；受限空范围时跳过存储并返回 inactive；仅在 `visibleMetricSeries(Principal(r), node, s)` 非空时置 active。将路由包装器改为 `permit` 以免绕过未知节点校验；`metricTarget` 的权限检查仍执行。`handleNodesLatest` 现有过滤只剔除已注册的范围外节点，未注册的 `ghost` 指标仍留在 `out`：将此处的授权判定改为 `!a.nodeInScope(p, name)` 时删除，受限用户既不见范围外节点，也不见无法归属节点；全局/无认证用户保持原行为。
- [ ] **Step 5: 跑接口回归测试**：`go test ./internal/server/api -run 'TestMetric|TestRoutes_Query' -count=1`，预期 PASS。

### Task 3: 统一 `hostname` 节点目标授权

**Files:** Modify `internal/server/api/auth.go:286-325`, `internal/server/api/query.go:179-183,219,951-1028`; Test `internal/server/api/scope_test.go`, `internal/server/api/scope_alert_test.go`。

**Interfaces:** Produces `func (a *API) permitHostname(next http.HandlerFunc, perm string) http.HandlerFunc`，将 `hostname` 作为唯一查询目标；`permitNode` 对同时提交且互相矛盾的 `node` / `hostname` 直接 400；维持原有路径参数语义。用 `a.checkNodeScope` 且确保受限未知目标不会进入 handler。

- [ ] **Step 1: 失败测试**：在真实路由表上对 `GET /api/v1/query/listeners?hostname=db-01`、`GET /api/v1/query/firewall?hostname=db-01`、`GET /api/v1/query/firewall/status?hostname=db-01`、`GET /api/v1/processes?hostname=db-01` 验证受限 `g1` 返回 403 且写入审计，不依赖缓存是否有值；`hostname=web-01&node=db-01` 返回 400，`hostname=ghost` 返回 403，不带 hostname 为 400，`hostname=web-01` 不应因权限返回 403。`/alerts?hostname=db-01&node=web-01` 返回 400，但仅 hostname 范围内仍通过现有结果过滤。

```go
for _, path := range []string{"/api/v1/query/listeners", "/api/v1/query/firewall", "/api/v1/query/firewall/status", "/api/v1/processes"} {
    rec := httptest.NewRecorder()
    newRoutesMux(a).ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"nodes:read"}, "g1"), http.MethodGet, path+"?hostname=db-01", ""))
    if rec.Code != http.StatusForbidden { t.Errorf("%s: %d", path, rec.Code) }
}
```

- [ ] **Step 2: 运行失败测试**：`go test ./internal/server/api -run 'TestRoutes_HostnameScope_|TestRoutes_AlertsConflictingNode_' -count=1`，预期至少一个 403 / 400 断言失败。
- [ ] **Step 3: 实现最小路由包装**：`permitHostname` 先 `checkPerm`，用 `hostname` 参数并检测同时提供的 `node` 不一致（400）；`hostname` 非空时调用 `checkNodeScope`，受限未知节点不得入 handler。为四个 endpoint 改包装器；`permitNode` 对 `alerts` 中同时存在 `node` 与 `hostname` 的矛盾做同一检查，并对显式未知节点拒绝访问。不要改变 `POST /report`、匿名状态页或其他不使用 hostname 的 endpoint。
- [ ] **Step 4: 运行所有相关用例**：`go test ./internal/server/api -run 'TestRoutes_Hostname|TestRoutes_Alerts|TestRoutes_NodeDetail|TestRoutes_Defense' -count=1`，预期 PASS。

### Task 4: 告警测试事件权限与前端按钮

**Files:** Modify `internal/server/api/query.go:321-323`, `internal/server/api/scope_ops_test.go`, `web/src/components/AlertsView.vue:88-93,512-524`; Create `web/src/components/AlertsView.test.js`。

**Interfaces:** 后端使用现有 `permit(a.handleAlertTest,"notify:write")`；前端使用现有 `useAuth().can`，不引入新 store。

- [ ] **Step 1: 后端失败测试**：`scope_ops_test.go` 里用 `scopeTestAPI`（其 engine=nil）：持有 `alerts:write` 但没有 `notify:write` 请求 `POST /api/v1/alerts/test` 返回 403 且有一条拒绝审计；持有 `notify:write` 返回 500（已进入 handler，engine=nil）；`authStore=nil` 返回 500（兼容关闭认证模式）。

```go
rec := httptest.NewRecorder()
newRoutesMux(a).ServeHTTP(rec, reqWith(globalPrincipal("alerts:write"), http.MethodPost, "/api/v1/alerts/test", ""))
if rec.Code != http.StatusForbidden { t.Fatalf("未授权不应触发通知: %d", rec.Code) }
```

- [ ] **Step 2: 跑测试确认 RED**：`go test ./internal/server/api -run TestRoutes_AlertTestNeedsNotifyWrite -count=1`，预期失败。
- [ ] **Step 3: 改路由为 `notify:write`**：仅更改告警测试路由包装；重新执行该测试，预期 PASS。
- [ ] **Step 4: 写前端失败测试**：`AlertsView.test.js` 用 `vi.mock('../api/http', ...)`、`vi.mock('echarts', ...)`、`vi.mock('./RuleModal.vue', ...)` 并 stub Element Plus 组件，`useAuth().principal` 在 mount 前设 `loaded=true`、只有 `alerts:read`，断言 `wrapper.text()` 不含「测试事件」；加 `notify:write` 时可见；`loaded=false` 的兼容模式可见。`afterEach` unmount 并 `useAuth().clear()`，避免全局状态泄漏。
- [ ] **Step 5: 运行 Vitest 验证失败再修复**：`npm --prefix web test -- AlertsView.test.js`；预期缺权限情况下错误显示按钮。`AlertsView.vue` 导入 `useAuth`，定义 `const { can } = useAuth()`，将该按钮改为 `v-if="can('notify:write')"`；重新运行，预期 PASS。必要时 stub timers/外部网络调用，勿放宽断言。

### Task 5: 前端跨节点浏览回归与说明文档

**Files:** Create `web/src/components/metrics/MetricsExploreView.test.js`; Modify `README.md:340-371,1452-1462`, `CONTEXT.md:98-99`。

**Interfaces:** 不变更组件接口；前端测试只验证请求参数、正常数据、403 提示，实际过滤由 Go 测试负责。

- [ ] **Step 1: 前端交互测试先写再跑**：mock `../../api/http` 的 `get`、`metricCatalog`、`metricActive`、默认导出的 `exportMetricCSV`，stub `../../charts/echarts` 的 `initChart`、`monitorOption`，stub `../../composables/useDashboards` 与 Element Plus 消息；挂载 `MetricsExploreView.vue` 后触发指标树节点事件。断言 `get` 收到 `/api/v1/query/range?metric=...` 且**没有** `node=`；点「导出 CSV」后断言 export 参数中无 node；`get` reject `Error('无权访问该节点分组')` 后可见「查询失败」提示。因项目已有请求形态，该测试首轮可能直接 PASS；若如此，记为基线契约测试而非强制改前端。

```js
expect(httpGet).toHaveBeenCalledWith(expect.stringMatching(/^\/api\/v1\/query\/range\?metric=/))
expect(http.exportMetricCSV).toHaveBeenCalledWith(expect.objectContaining({ metric: 'cpu_usage' }))
```

- [ ] **Step 2: 修订文档**：README 已实现特性 `README.md:153` 与路线图 `README.md:347-348,370-371,376` 互相矛盾；删除对应“未实现”重复项，未实现的日志高级分析 / 容量预测与高可用保留为“增强”而非“从无到有”。`README.md:360,1462` 和 `CONTEXT.md:99` 改为“业务路由已配置权限点、节点分组范围在服务端校验；本次补齐指标、hostname、测试告警权限边界”。不要声称所有权限无遗漏；不要删除未来功能的路线图。
- [ ] **Step 3: 完整验证**：`go test -count=1 ./...`、`go vet ./...`、`npm --prefix web test`、`npm --prefix web run build`，预计均返回 0；`git diff --check` 检查空格和冲突；报告实际输出。构建脚本会执行 `version:sync`，先检查 `git status --short`，若生成文件变化，仅在确认它是本次构建产物时恢复该文件，切勿触碰既有未跟踪内容。

## 交接与分批

任务 1–3 是同一权限边界的依赖链，任务 4–5 独立于 TSDB 实现；每项完成均需运行该项测试再进入下一项。保留设计文档及本计划在工作区供审批；用户没有要求提交，执行时不自行 commit/push。Server 生命周期另见 `docs/superpowers/plans/2026-09-27-server-lifecycle.md`，两个计划可分别审核与执行。
