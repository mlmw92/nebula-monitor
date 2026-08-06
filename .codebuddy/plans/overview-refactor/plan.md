## 产品概述

对首页概览(OverviewView)进行全面重构，使其从当前"健康度+KPI+节点状态+告警"四区块的稀疏布局，升级为信息丰满、模块可编排、覆盖主机与中间件的统一监控入口。

## 核心功能

1. **系统健康度瘦身**：将健康度面板从 1.2fr 巨型布局缩小为紧凑横条卡片，健康度环形图+评分+在线率/活跃告警/离线数合并为单行紧凑展示，释放空间给更多内容区块。
2. **主机概览(原"节点状态")按分组横向排列**：标题改为"主机概览"；主机按 group 字段分组，每组一个区块标题+组内主机卡片 flex-wrap 自动换行横向排列；主机卡片名称展示 displayName(别名)，为空时回退 hostname。
3. **中间件概览区块**：新增中间件概览区域，每种中间件(Redis/MySQL/PostgreSQL/Nginx/Kafka/Docker/RocketMQ/K8s)一张汇总卡(实例总数/在线/告警数)，并展示各类型在线实例 TopN 排行(内存占用/连接数/QPS 等)+状态环形图，点击跳转 `/middleware` 对应 Tab。
4. **自定义仪表盘(模块可显隐/排序)**：首页提供"编辑模式"按钮，进入后可勾选显示哪些区块(健康度/KPI/主机概览/中间件概览/紧急告警/最近告警)并拖拽排序，配置持久化到 localStorage，后端无需改动。
5. **KPI 指标行紧凑化**：主机总数/在线/离线/告警 KPI 与中间件实例总数/在线/异常 KPI 合并为统一的 KPI 指标行，复用现有 KpiCard 组件。

## 技术栈

- 前端：Vue3 (Composition API, `<script setup>`) + Element Plus + 现有 CSS 变量主题 (dark glassmorphism)
- 构建工具：Vite（改动后需 `cd web && npm install && npm run build`）
- 数据源：全部复用现有后端 API，零后端改动
- `GET /api/v1/nodes` → 节点列表(含 hostname/group/displayName/ip/os/status)
- `GET /api/v1/nodes/latest` → 节点最新指标(含 cpu/mem/disk/load1)
- `GET /api/v1/groups` → 分组列表(决定分组顺序)
- `GET /api/v1/alerts?state=active` → 活跃告警
- `GET /api/v1/middleware/{redis|mysql|postgres|nginx|kafka|docker|rocketmq|k8s}/instances` → 8 类中间件实例聚合数据
- 持久化：localStorage（自定义仪表盘配置，key: `nebula_overview_layout`）

## 实现方案

### 整体策略

纯前端重构 `OverviewView.vue`，拆分为多个子组件以降低单文件复杂度，复用已有的 `KpiCard.vue`、`OsIcon.vue` 及中间件图标资源。新增一个 composable 管理"模块显隐/排序"的编辑模式。

### 关键技术决策

1. **健康度瘦身**：将 `top-grid` 从 `grid-template-columns: 1.2fr 0.8fr 1fr` 改为紧凑的横向 KPI 行，健康度面板内联为 KpiCard 同等高度的窄条(健康度环形图缩小到 80px，评分+状态右移内联)，不再独占 1.2fr 大面积。

2. **主机概览分组布局**：

- 数据：从 `GET /api/v1/nodes` 获取节点列表后，按 `node.group` 分组；分组顺序以 `GET /api/v1/groups` 返回顺序为准，未在 groups 列表的节点归入"未分组"末尾。
- 展示：每组一个区块(`.group-section`)，包含分组标题(分组名+在线/总数小标签)和主机卡片 flex-wrap 容器(`.group-hosts`：`display: flex; flex-wrap: wrap; gap: 10px`)，组内主机卡片横向排列超出自动换行。
- 别名展示：新增 `displayName(n)` 计算函数返回 `n.displayName || n.hostname`，卡片标题、IP 均用此函数。
- 排序：保持现有的异常节点置顶逻辑(`nodeSeverity`)，但仅在组内排序。

3. **中间件概览区块**：

- 数据加载：`load()` 函数新增 `Promise.allSettled` 并行请求 8 个中间件实例接口，各自 `.catch(() => ({ instances: [] }))` 容错。
- 汇总卡：每种中间件一张卡，显示图标+类型名+实例总数/在线数/告警数(告警数=up=false 的实例数)。无实例的类型不展示该卡。
- TopN 排行：每种中间件取在线实例按关键指标排序取 Top3(Redis 按 usedMemory、MySQL 按 threadsConnected、Postgres 按 numbackends、Nginx 按 activeConnections、Kafka 按 consumerLag、Docker 按 memPercent、RocketMQ 按 messageAccumulation、K8s 按 podsTotal)，展示实例名+指标值+迷你进度条。
- 状态环形图：每个汇总卡内嵌一个小的在线/离线比例环形 SVG（复用健康度环形图模式缩小）。
- 点击交互：汇总卡和 TopN 行点击跳转 `router.push('/middleware')`。

4. **自定义仪表盘(编辑模式)**：

- 配置数据结构：`{ modules: [{ id: 'health', label: '系统健康度', visible: true }, { id: 'kpi', ... }, { id: 'hosts', ... }, { id: 'middleware', ... }, { id: 'critical-alerts', ... }, { id: 'recent-alerts', ... }], order: ['health','kpi','hosts','middleware','critical-alerts','recent-alerts'] }`
- 存储：`localStorage.setItem('nebula_overview_layout', JSON.stringify(config))`，页面加载时读取，无配置时用默认全显。
- 编辑模式 UI：右上角"自定义"按钮 → 切换 `editMode=true`，各区块出现拖拽手柄+显隐 checkbox+上下移动按钮；区块使用 `v-for` 按 order 渲染，拖拽用简单的"上移/下移"按钮（不引入第三方拖拽库，保持 KISS）。
- 非编辑模式下，`visible=false` 的区块不渲染，`order` 决定渲染顺序。

### 性能考量

- 中间件 8 个 API 并行请求 + 现有 3 个 API = 11 个并行请求，用 `Promise.allSettled` 避免单个失败阻塞全部。
- 30s 轮询保持不变，但中间件请求在首次加载后才发起(可通过 `loadMiddleware()` 独立函数+独立 timer 控制，避免与主机指标混在一起增加单次请求延迟)。实际实现中将中间件请求也放入 `load()` 的 `Promise.allSettled` 中一次完成，因为都是非阻塞并行。
- 主机分组用 `computed` 自动响应 nodes 变化，避免手动 watch。

### 架构设计

```
OverviewView.vue (容器：编辑模式控制 + 布局编排 + 数据加载)
├── KpiRow.vue           [NEW] KPI 指标行(主机+中间件合并 KPI，复用 KpiCard)
├── HealthBar.vue        [NEW] 紧凑健康度横条(环形图+评分+状态内联)
├── HostOverview.vue     [NEW] 主机概览(按分组 flex-wrap 横向排列，别名展示)
├── MiddlewareOverview.vue [NEW] 中间件概览(汇总卡+TopN+状态环形图)
├── CriticalAlerts.vue   [NEW] 紧急告警摘要(从 OverviewView 抽出)
└── RecentAlerts.vue     [NEW] 最近告警列表(从 OverviewView 抽出)
```

使用 `useOverviewLayout` composable (`web/src/composables/useOverviewLayout.js` [NEW]) 管理 localStorage 读写与模块排序/显隐逻辑。

### 目录结构

```
nebula-monitor/
└── web/
    └── src/
        ├── components/
        │   ├── OverviewView.vue           [MODIFY] 重构为容器组件，管理编辑模式+布局编排+数据加载；原 inline 模板/script/style 拆分到子组件
        │   ├── overview/                   [NEW] 目录
        │   │   ├── KpiRow.vue              [NEW] KPI 指标行；合并主机KPI(总数/在线/离线/告警)与中间件KPI(实例总数/在线/异常)；复用 KpiCard 组件
        │   │   ├── HealthBar.vue           [NEW] 紧凑健康度横条；环形图缩至80px+评分+在线率/活跃告警/离线数内联单行；从 OverviewView 原 health-panel 逻辑迁移
        │   │   ├── HostOverview.vue        [NEW] 主机概览；按 group 分组，每组标题+flex-wrap 主机卡片横向排列；卡片名称用 displayName 回退 hostname；复用 OsIcon
        │   │   ├── MiddlewareOverview.vue  [NEW] 中间件概览；8类中间件汇总卡(图标+总数/在线/告警)+TopN排行+状态环形图；点击跳转 /middleware
        │   │   ├── CriticalAlerts.vue      [NEW] 紧急告警摘要；从 OverviewView 原模板抽出
        │   │   └── RecentAlerts.vue        [NEW] 最近告警列表；从 OverviewView 原模板抽出
        │   ├── KpiCard.vue                 [复用] 无改动
        │   └── OsIcon.vue                  [复用] 无改动
        ├── composables/                    [NEW] 目录
        │   └── useOverviewLayout.js        [NEW] 管理首页模块显隐/排序配置；读写 localStorage('nebula_overview_layout')；提供 defaultLayout/editMode/moveModule/toggleVisible/saveLayout
        └── assets/
            └── img/                        [复用] redis/mysql/postgresql/nginx/Kafka/docker/rocketMQ/kubernetes.svg
```

## 实现备注

- **数据加载**：`OverviewView.vue` 的 `load()` 函数扩展为 `Promise.allSettled` 并行请求 nodes + alerts + latest + groups + 8个中间件接口，单个 `.catch` 容错返回空数组，避免任一接口异常导致首页白屏。
- **别名展示规则**：`displayName(n) = n.displayName?.trim() || n.hostname`，与后端 `SetNodeDisplayName` 逻辑一致(空串=清除别名回退 hostname)。
- **分组逻辑**：`computed` 中先按 `/api/v1/groups` 顺序建立分组桶，遍历 nodes 填入对应桶，无匹配分组的归入"未分组"桶。排序：异常节点在组内置顶(复用现有 `nodeSeverity` 函数)。
- **中间件指标映射**：TopN 排行指标需按类型区分——Redis:usedMemory、MySQL:threadsConnected、Postgres:numbackends、Nginx:activeConnections、Kafka:consumerLag、Docker:memPercent、RocketMQ:messageAccumulation、K8s:podsTotal。数值格式化复用 RedisTab 中已有的 `formatBytes`/`formatNum` 逻辑(在子组件内内联简洁版)。
- **编辑模式持久化**：localStorage key `nebula_overview_layout`，结构含 `modules` 数组(id/label/visible)和 `order` 数组。`onMounted` 时读取并合并默认值(新增模块自动补入末尾)，编辑模式退出时自动保存。
- **CSS 变量复用**：所有子组件沿用 `--accent`/`--warn`/`--danger`/`--bg-card`/`--border`/`--text`/`--text-dim`/`--mono`/`--chart-*` 等现有变量，保持视觉一致性。
- **响应式**：保持 `@media (max-width: 1200px)` 和 `@media (max-width: 1100px)` 断点，中间件汇总卡网格在窄屏自动从多列变单列。
- **构建与提交**：改完 `cd web && npm install && npm run build`；纯前端改动无需 cross-compile.sh 和重新分发 agent；但如需发布新版本需同步 VERSION 文件并重新编译 server 二进制(因前端 embed)。提交前 `git status` 检查工作区避免混入无关改动。

## 设计风格

延续项目现有的深色 Glassmorphism 风格(毛玻璃面板+暗色背景+霓虹辉光)，在首页概览中通过紧凑布局和信息密度提升让页面更丰满。健康度从大面积面板降级为横条卡片，释放空间给主机分组概览和中间件概览两大新区块。编辑模式提供清爽的模块管理交互。

## 页面区块设计(自上而下)

1. **顶部工具栏**：左侧页面标题"监控概览"+右侧"自定义"按钮(编辑模式入口)
2. **KPI 指标行**：7-8 张 KpiCard 横向排列(主机总数/在线/离线/告警 + 中间件实例/在线/异常)，紧凑 min-height
3. **系统健康度横条**：80px 环形图+评分数字+健康标签+在线率/活跃告警/离线数内联，单行卡片高度与 KpiCard 齐平
4. **主机概览**：按分组纵向堆叠，每组标题(分组名+在线数/总数徽章)+组内主机卡片 flex-wrap 横向排列；卡片含 LED 状态灯+OS 图标+别名+IP+CPU/MEM/DISK 迷你进度条
5. **中间件概览**：8 类中间件汇总卡网格(每卡：图标+类型名+实例总数/在线/告警+迷你状态环形图)+下方 TopN 排行行(每类 1 行，展示前 3 实例名+指标值+迷你进度条)
6. **紧急告警 + 最近告警**：左右双栏，紧凑列表，保持现有样式

## 编辑模式交互

- 点击"自定义"按钮 → 各区块右上角出现 checkbox(显隐)+上移/下移按钮
- 拖拽手柄改用上下箭头按钮(KISS，不引入拖拽库)
- 底部出现"完成"按钮，点击退出编辑模式并保存到 localStorage

## Agent Extensions

### SubAgent

- **code-explorer**: 在实现阶段用于快速定位 OverviewView 的引用关系（如 MainLayout 中是否有 reload 调用、router 中 overview 路由配置），确保拆分子组件后不遗漏调用链。预期结果：确认 OverviewView 的所有外部依赖点，拆分后 reload/refresh 机制正常工作。