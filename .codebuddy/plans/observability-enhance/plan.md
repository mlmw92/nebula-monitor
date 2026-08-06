# 可观测性增强：自定义仪表盘 / 指标自动发现 / 历史数据导出

## 用户需求
三个能力全部实现：
1. **自定义仪表盘**：用户可自行编排图表（选指标、选节点/实例、图表类型、时间范围），保存为个人看板，与内置概览并存。
2. **指标自动发现**：系统自动探测已被采集上报的指标全集（按分类归组），用户无需手敲 PromQL 即可从中挑选；对中间件实例也能列出其真实指标。
3. **历史数据导出**：对任意查询（节点+指标+时间范围）导出为 CSV（必要时 XLSX），供离线分析。

## 设计总览

三者共用一个底座：**统一指标目录（Metric Catalog）**。自动发现本质是"采集并暴露这个目录"；自定义仪表盘从中挑选指标；历史导出对该目录中的指标做范围查询后落盘。

### 1. 统一指标目录（底座）
新建 `internal/server/metrics/catalog.go` + `catalog_register.go`：
- `MetricMeta{ Name, Title(中文), Category(主机/CPU/内存/磁盘/网络/中间件:Redis...), Unit, ChartType(line/area/bar/gauge/pie), PromQLTemplate }`。
- 各 collector 在初始化时调用 `catalog.Register(...)` 注册自身指标（低风险：纯追加注册，不改采集逻辑）。主机指标在 `metrics/host_catalog.go` 硬编码（cpu_usage/mem_used_percent/disk_*/network_*/load* 等，来源 NodeView 现有指标名）。
- `GET /api/v1/metrics/catalog` 返回按 category 分组的指标清单（含中文名、单位、推荐图表类型），供前端"自动发现"面板与仪表盘指标选择器使用。
- 中间件实例级：对 `up` 类指标与 `mwSummarySpecs` 已有的 metric 名一并登记，使"自动发现"能覆盖中间件。

### 2. 指标自动发现（API + UI）
- 后端：`GET /api/v1/metrics/catalog`（上条）即为主入口；另加 `GET /api/v1/metrics/active?category=&node=` 返回"当前确实有数据上报"的指标（基于 `QueryAllLatest` 探测非空），区分"已注册但无数据"与"有数据"，UI 标记在线状态。
- 前端：新增「指标浏览 / 自动发现」页（`MetricsExploreView.vue`）：左侧分类树（来自 catalog），右侧点击指标即出其近 1h 趋势（复用 `charts/echarts.js` 的 `setHistory`），并带"添加到仪表盘"按钮。

### 3. 自定义仪表盘
**服务端**：
- `Config` 新增 `DashboardsFile`（默认 `/etc/monitor-server/dashboards.yaml`）。
- `internal/server/dashboard/store.go`：`Dashboard{ ID, Name, Layout, Panels[] }`，`Panel{ Title, ChartType, Metric, Node(或 instance+type), Labels, Range, Step }`；`Load/Save`（YAML 落盘，atomic write，先备份 .bak）；`List/Get/Create/Update/Delete`。
- API（新建 `internal/server/api/dashboard.go`）：
  - `GET/POST /api/v1/dashboards` 列表/新建
  - `GET/PUT/DELETE /api/v1/dashboards/{id}` 详情/改/删
  - 复用 `ui/settings` 同款"登录态 + 持久化"模式，保存即落盘热生效，无需重启。
**前端**：
- 新增「自定义仪表盘」页（`DashboardView.vue`）：看板列表 + 编辑模式（网格布局）；每个 Panel 用 `panel-chart` 子组件按 `ChartType` 调 `echarts` 渲染，数据走现有 `/api/v1/query/range`。
- `composables/useDashboards.js`：单例 reactive + localStorage 缓存（仿 `useBrand.js`）。
- 侧边栏新增入口；路由 `system/dashboards`。

### 4. 历史数据导出
**服务端**（新建 `internal/server/api/export.go`）：
- `GET /api/v1/metrics/export?metric=&node=&start=&end=&step=&format=csv`：
  - 调 `store.QueryRange` 取 `[]model.Series`；
  - 多序列时以 `timestamp, labels, value` 长表格式（CSV 通用、易用 Excel 打开）；单序列简化为 `timestamp,value`；
  - 设置 `Content-Type: text/csv; charset=utf-8` 与 `Content-Disposition: attachment; filename=...` 触发浏览器下载（仿 `handleRulesExport` 的下载写法）；
  - 加基础限流/范围校验（start/end 必填、跨度上限防滥用）。
- 可选 XLSX：工期紧先用 CSV；XLSX 留作后续（如需，引入轻量库或手写简易 xlsx）。
**前端**：
- 指标浏览页、节点详情、仪表盘 Panel 均加"导出 CSV"按钮，拼上述 URL（带 token 的 download 用 `<a>` + `Authorization` 头需 fetch blob 再下载，因 token 在 header）。

## 受影响文件
```
internal/server/
├── metrics/catalog.go            # [NEW] 指标目录模型 + Register/List
├── metrics/host_catalog.go       # [NEW] 主机指标注册（cpu/mem/disk/net/load/swap/proc）
├── metrics/register_collectors.go# [NEW] 在 server 启动时汇总注册各模块指标
├── dashboard/store.go            # [NEW] 仪表盘 YAML 存储（CRUD + 落盘备份）
├── api/catalog.go                # [NEW] GET /api/v1/metrics/catalog, /active
├── api/dashboard.go             # [NEW] 仪表盘 CRUD 接口
├── api/export.go                # [NEW] GET /api/v1/metrics/export (CSV)
├── api/query.go                 # [MODIFY] 路由注册上述新接口
├── config/config.go            # [MODIFY] 新增 DashboardsFile 字段 + 加载
└── main.go                     # [MODIFY] 初始化 catalog + dashboard store 并注入 API
web/src/
├── api/http.js                 # [MODIFY] 加 catalog/dashboard/export 请求封装
├── composables/useDashboards.js# [NEW] 仪表盘单例状态
├── components/metrics/MetricsExploreView.vue  # [NEW] 指标浏览/自动发现
├── components/dashboard/DashboardView.vue     # [NEW] 自定义仪表盘
├── components/dashboard/PanelChart.vue        # [NEW] 面板图表（复用 echarts 封装）
├── router/index.js             # [MODIFY] 加路由
└── components/Sidebar.vue      # [MODIFY] 加导航项
```

## 兼容性 / 风险
- catalog 注册为纯追加，不改现有采集与查询路径，零回归风险。
- 仪表盘 YAML 默认文件不存在时 `Load` 返回空列表（不报错），升级无痛。
- 导出接口复用现有 `QueryRange`，无新存储依赖；加范围上限防大查询拖垮时序库。
- Agent 无改动，无需重分发 agent 二进制。

## 验证
- 单测：`metrics` 注册/列出；`dashboard` store 增删改查 + 落盘；`export` CSV 格式。
- 手动：启动 server → 指标浏览页能看到主机/中间件分类指标且点开有趋势 → 新建仪表盘加两个 Panel 保存刷新仍在 → 节点详情/浏览页导出 CSV 能下载且 Excel 可打开。

## 提交策略
分 3 个 commit（底座 catalog → 仪表盘 → 自动发现+导出），每步编译+前端构建通过，最后统一打包升级（upgrade 包，由用户在 Web 端应用）。
