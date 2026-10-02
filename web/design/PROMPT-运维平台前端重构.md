# 提示词：NebulaEye 运维平台前端视觉重构

> **用法**：把本文件全文复制，作为提示词交给执行模型。执行前必须先打开参考设计稿：
> - 框架与首页：`design/redesign-preview.html`（单文件，浏览器打开，右上角可切三套主题）
> - **其余页面：`design/pages-redesign.html`（六个 tab：列表页 / 详情页 / 告警中心 / 设置表单 / 四态与密度 / 页面清单）——做批次 6 时必看**
>
> 设计稿是视觉真源，本文件是施工规格；两者冲突时以设计稿为准，并在回复中注明。
>
> **当前进度（2026-10-01）**：批次 0–5 已落地（令牌层、四个规范件、`AppTopBar`、`CommandPalette`、侧栏 232px、首页概览 + `TriageBar` + `useTriage`）。
> **本轮只做批次 6。** 执行前先 `git status` 确认上述文件存在，不要重做已完成的部分。

---

## 一、任务目标与边界

**要做的**：在**不改动任何业务逻辑、接口调用、路由、权限判断**的前提下，重做运维平台的视觉层——设计令牌、主框架（侧栏/顶栏）、首页概览，以及四个全站复用的规范件。

**不做的**（明确禁止，不要自作主张扩展）：

- ❌ 不做全站浅色模式（成本收益不成立，见附录 B）
- ❌ 不动数据大屏（`src/components/screen/**`）—— 本轮完全跳过
- ❌ 不改动任何 `/api/**` 调用、请求参数、响应字段处理
- ❌ 不改动 `src/router/index.js` 的 `path` / `name` / `meta.perm`
- ❌ 不新增任何 npm 依赖（Vue 3 + Vite + Element Plus + ECharts 已够用）
- ❌ 不重构组件的数据流（computed / watch / 生命周期保持原样）
- ❌ 不新增英文界面文案——界面全中文，新增文案一律中文

**核心判断**：这是"换皮 + 立体系"，不是重写。判断标准是——改完之后，任何一个页面的功能行为都必须和改之前完全一致，只是更好看、更好用。

---

## 二、项目现状（已核实的事实，可直接采信）

| 项 | 事实 |
|---|---|
| 技术栈 | Vue 3（`<script setup>`）+ Vite + Element Plus + `vue-echarts` |
| 入口 | `src/main.js` → `src/App.vue` → `src/router/index.js` |
| 布局 | `src/layouts/MainLayout.vue` + `src/components/Sidebar.vue` + `SiteFooter.vue` |
| 全局样式 | `src/assets/style.css`（590 行，唯一全局样式源） |
| 组件规模 | **93 个 `.vue` 文件** |
| 主题 | `body[data-theme]` 三套：`b` 极光蓝（默认）/ `a` 星云青绿 / `c` 星河紫；切换逻辑在 `MainLayout.vue` 的 `applyTheme()`，存 `localStorage['nebula_theme']`，切换后 `window.dispatchEvent(new CustomEvent('nebula:theme-changed'))` |
| 暗色 | `src/main.js:40` 无条件 `document.documentElement.classList.add('dark')`；Element Plus 只引了 `dark/css-vars.css` |
| 首页 | `src/components/OverviewView.vue`，12 栅格 + 7 个可拖拽 block，布局持久化在 `src/components/overview/useOverviewLayout.js` |
| 路由 | hash 模式，共 22 个业务路由 + `/login` `/screen` `/status` |

### 现状的五个问题（这是本次要解决的）

1. **导航效率低**：侧栏仅 `190px`，6 个分组默认只展开当前所在分组（`Sidebar.vue` 的 `openGroups` 只在 `onMounted` 设一次，**路由切换后不更新**），22 个页面全靠逐层点开；折叠态 64px 无 tooltip，只剩图标。
2. **顶栏噪音重**：6 个圆形图标按钮并列；"数据大屏"入口是紫渐变 + 流光 + 闪烁动画（`MainLayout.vue` 的 `.screen-entry`，含 3 个 keyframes），是全站最吵的一块。
3. **容器语言不统一**：`.glass` / `.kpi` / `.ov-card` / `.panel` 四种并存，圆角 12/12/14/12 各异，**全部无阴影**，深色下层几乎消失。
4. **页面头各自为政**：`OverviewView` 用 22px 渐变文字标题，其余页面 17/20/22px 混用，没有统一页头组件。
5. **无体系**：无全局搜索、无空状态规范、间距与字号任意取值（出现 10/14/18 这类非栅格值）。

### 硬编码盘点（已实测，不是估算）

`rgba(255,255,255,x)` / `#fff` 共 **45 个文件、176 处**。Top 名单：

```
27  src/components/redis/RedisTab.vue
19  src/assets/style.css
10  src/components/HostsView.vue
 9  src/components/SecurityView.vue
 9  src/components/screen/MiddlewareMonitorPanel.vue   ← 大屏，本轮跳过
 8  src/components/NodeView.vue
 7  src/components/screen/tabs/NetworkTab.vue          ← 大屏，本轮跳过
 6  src/layouts/MainLayout.vue
 5  src/components/asset/AssetListView.vue
 5  src/components/screen/InstanceDetailDialog.vue     ← 大屏，本轮跳过
 4  src/components/Sidebar.vue
 4  src/components/mysql/MySQLTab.vue
```

---

## 三、设计原则（违反即返工）

1. **深色下用"表面层级 + 阴影"建层次，不用背景透明度。** 现状四层容器全靠 `rgba(15,24,43,.92)` 这类透明度区分，深色下差异几乎不可见。新方案：四级表面 `#s0→#s3` 亮度递增 + 三级阴影 + 1px 描边。
2. **颜色只表达状态，不做分类配色。** 这是 2026 年最硬的共识。KPI 卡不要给四张卡配蓝/红/绿/橙四种图标底色——图标统一中性色，**只有真实异常才允许出现语义色**。给 5 个 KPI 配 5 种颜色是被明确淘汰的模式。
3. **专业工具要密度，不要"呼吸感"。** 圆角 16→10px、卡片内边距 24→16px、表格行高收紧。2022 年的大圆角大留白在运维平台里是负担。
4. **克制光效。** 顶部受光高光 4.5%→**2.2%**，阴影减重，边界主要交给 1px 描边。不要发光字、不要流光、不要粒子。
5. **砍掉所有装饰性动画。** `.screen-entry` 的 shimmer / pulse / blink 三个 keyframes 全部删除。动效只服务于状态反馈（hover 0.12s、展开 0.2s、入场 0.36s）。
6. **统一节奏。** 全站页头、卡片、空状态、状态标签各只有一个实现。发现某个页面自己写了一套，就是没做完。
7. **中文优先。** 所有新增文案中文；中文字距控制在 0.08–0.14em（中文在 ≥0.2em 字距下会散架）。

---

## 四、设计令牌

### 4.1 在 `src/assets/style.css` **追加**（不要删除任何现有变量）

现有变量名（`--bg` `--bg-card` `--border` `--text` `--text-dim` `--accent` `--danger` `--warn` `--info` `--radius` `--sidebar-w` 等）**必须全部保留**，用别名指向新令牌，保证 93 个存量组件零改动即可受益。

```css
:root,
body[data-theme="b"] {
  /* —— 表面层级（新增） —— */
  --s0:#070d1a;  --s1:#0b1220;  --s2:#101a2e;  --s3:#16203a;
  /* —— 向后兼容别名（保留旧名，指向新值） —— */
  --bg:var(--s0); --bg-elev:var(--s1); --bg-card:var(--s1);

  /* —— 描边 —— */
  --bd:rgba(120,170,255,.10);
  --bd-strong:rgba(120,170,255,.20);
  --border:var(--bd); --border-strong:var(--bd-strong);

  /* —— 文本三级 —— */
  --t1:#e8eefb; --t2:#9baed0; --t3:#6f83a6;
  --text:var(--t1); --text-main:var(--t1); --text-dim:var(--t2);
  --text-muted:var(--t3); --label:#b0c0dd;

  /* —— 语义色：主色 + 四态，各含 -dim(14%) 与 -bd(32%) —— */
  --accent:#4a9df0; --accent-dim:rgba(74,157,240,.12); --accent-glow:rgba(74,157,240,.16);
  --ok:#22c55e;     --ok-dim:rgba(34,197,94,.14);      --ok-bd:rgba(34,197,94,.32);
  --warn:#ffb454;   --warn-dim:rgba(255,180,84,.14);   --warn-bd:rgba(255,180,84,.32);
  --danger:#ff5d6c; --danger-dim:rgba(255,93,108,.14); --danger-bd:rgba(255,93,108,.32);
  --info:#38bdf8;   --info-dim:rgba(56,189,248,.14);   --info-bd:rgba(56,189,248,.32);

  /* —— 尺度（4px 栅格，禁止出现 10/14/18 这类零碎值） —— */
  --sp-1:4px; --sp-2:8px; --sp-3:12px; --sp-4:16px; --sp-5:20px; --sp-6:24px; --sp-8:32px;
  --r-xs:6px; --r-sm:8px; --r-md:10px; --r-lg:14px; --r-pill:999px;
  --sh-1:0 1px 2px rgba(0,0,0,.35);
  --sh-2:0 4px 14px rgba(0,0,0,.38);
  --sh-3:0 12px 34px rgba(0,0,0,.48);
  --fs-xs:12px; --fs-sm:13px; --fs-md:14px; --fs-lg:15px;
  --fs-xl:17px; --fs-2xl:20px; --fs-3xl:24px;
  --dur-1:.12s; --dur-2:.2s; --dur-3:.36s;
  --ease:cubic-bezier(.4,0,.2,1);
  --sidebar-w:232px;          /* 190 → 232 */
  --radius:var(--r-md);

  /* —— 通用填充令牌（用于收敛硬编码白色） —— */
  --fill-1:rgba(120,170,255,.04);
  --fill-2:rgba(120,170,255,.06);
  --fill-3:rgba(120,170,255,.09);
  --fill-hover:rgba(120,170,255,.07);
}

body[data-theme="a"] {
  --s0:#0a0e14; --s1:#10151d; --s2:#161c26; --s3:#1d2430;
  --bd:rgba(255,255,255,.08); --bd-strong:rgba(255,255,255,.14);
  --t1:#e6edf3; --t2:#a4adba; --t3:#778594;
  --accent:#00d9a3; --accent-dim:rgba(0,217,163,.12); --accent-glow:rgba(0,217,163,.16);
  --fill-1:rgba(255,255,255,.04); --fill-2:rgba(255,255,255,.06); --fill-3:rgba(255,255,255,.09);
  --fill-hover:rgba(255,255,255,.07);
}

body[data-theme="c"] {
  --s0:#0b0916; --s1:#120e24; --s2:#181330; --s3:#1f183c;
  --bd:rgba(170,130,255,.10); --bd-strong:rgba(170,130,255,.20);
  --t1:#ece9fb; --t2:#aaa3ce; --t3:#8079a0;
  --accent:#8b5cf6; --accent-dim:rgba(139,92,246,.12); --accent-glow:rgba(139,92,246,.16);
  --fill-1:rgba(170,130,255,.05); --fill-2:rgba(170,130,255,.07); --fill-3:rgba(170,130,255,.10);
  --fill-hover:rgba(170,130,255,.08);
}
```

**语义色（`--ok/--warn/--danger/--info`）三套主题共用一套值，不要重复定义。**

### 4.2 统一容器（替代 `.glass` / `.kpi` / `.ov-card` / `.panel` 四套）

```css
.card{
  background:var(--s1);
  border:1px solid var(--bd);
  border-radius:var(--r-md);        /* 10px */
  box-shadow:var(--sh-1);
  position:relative;
  transition:box-shadow var(--dur-1) var(--ease), transform var(--dur-1) var(--ease);
}
.card::before{                       /* 顶部受光高光，仅 2.2% */
  content:''; position:absolute; left:1px; right:1px; top:0; height:1px;
  background:linear-gradient(90deg,transparent,rgba(255,255,255,.022),transparent);
  pointer-events:none;
}
.card:hover{ box-shadow:var(--sh-2); transform:translateY(-1px); }
```

`.glass` / `.kpi` / `.panel` 保留原类名（存量组件在用），但内部改为复用上面的值；新建的 `SectionCard.vue` 直接用 `.card`。

### 4.3 Element Plus 同步

`html.dark` 块内的 `--el-*` 已有覆盖，新增令牌后要同步这几项（其余保持原样）：

```css
--el-bg-color:var(--s1);
--el-bg-color-overlay:var(--s3);
--el-border-color:var(--bd);
--el-border-color-light:var(--bd);
--el-border-color-lighter:var(--bd);
--el-fill-color:var(--fill-2);
--el-fill-color-light:var(--fill-1);
--el-fill-color-lighter:var(--fill-1);
--el-fill-color-blank:var(--s2);
--el-border-radius-base:var(--r-sm);   /* 8px */
```

---

## 五、四个规范件（新建 `src/components/common/`）

### 5.1 `PageHeader.vue`

```vue
props: { title:String, desc:String }
slot:  actions      // 右对齐操作区
```
- 标题 `--fs-3xl` 24px / 600 / `--t1`，**不要渐变文字**
- 描述 13px / `--t3` / 上间距 4px
- 布局：`display:flex; justify-content:space-between; align-items:flex-start; gap:16px`，底部 `margin-bottom:20px`

### 5.2 `SectionCard.vue`

```vue
props: { title:String, subtitle:String, dense:Boolean }
slot:  default, actions, footer
```
- 容器用 `.card`；`dense` 时 body 内边距 12px（默认 16px）
- 头部：`padding:12px 16px`，底部 1px 描边，标题 14px/600，左侧 3px 高 14px 的 `--accent` 竖条
- 无 title 时不渲染头部

### 5.3 `StatusPill.vue`

```vue
props: { tone:{ type:String, default:'muted' },  // ok|warn|danger|info|muted
         dot:Boolean, size:{ type:String, default:'sm' } }
slot:  default
```
- 12px 轻量药丸：`padding:2px 8px; border-radius:var(--r-pill)`
- 背景用对应 `--{tone}-dim`，文字用 `--{tone}`，`muted` 用 `--fill-2` + `--t3`
- `dot` 时在文字前渲染 6px 圆点

### 5.4 `EmptyState.vue`

```vue
props: { icon:[Object,String], title:String, hints:Array, actionText:String }
emit:  action
```
- 居中，图标 32px `--t3`，标题 14px `--t2`，`hints` 逐行 12.5px `--t3`
- **必须带排查建议**（这是设计要点：空状态要告诉用户下一步做什么，不能只写"暂无数据"）

---

## 六、分批实施

> 每批结束后**必须**跑一次 `npm run build` 确认通过，再进入下一批。
> 每批都要能独立上线，不出现"改到一半破版"的中间态。

### 批次 0 · 令牌层（零风险，约 1 小时）

**文件**：`src/assets/style.css`

1. 按 4.1 追加令牌（现有变量一个都不删）
2. 按 4.2 新增 `.card`；把 `.glass` / `.kpi` / `.panel` 的值改为复用新令牌
3. 按 4.3 同步 `--el-*`
4. 把 `style.css` 自身那 19 处硬编码白色替换为 `--fill-*` / `--bd`

**验收**：`npm run build` 通过；三套主题切换后卡片、表格、按钮颜色都跟随变化；页面无破版。

### 批次 1 · 四个规范件（约 2 小时）

**文件**：新建 `src/components/common/{PageHeader,SectionCard,StatusPill,EmptyState}.vue`

**验收**：四个组件在 `OverviewView` 里手动引用一次能正常渲染；不改动任何现有页面。

### 批次 2 · 侧栏（约 4 小时）

**文件**：`src/components/Sidebar.vue`（样式与模板），`src/layouts/MainLayout.vue` 的 `.main-wrap.collapsed` 断点值

要点：
1. 宽度 `190px → 232px`（`--sidebar-w`），折叠态 64px 不变
2. **分组默认全部展开**：删掉 `openGroups` 的"只展开当前分组"逻辑，改为 `ref({})` + `isGroupOpen(g)` 默认返回 `true`，用户手动收起后再按用户状态。同时**用 `watch` 监听 `route.path`，路由切换时确保目标分组是展开的**
3. 活动项左侧 **3px 光条**（`.nav-subitem.active::before`，`background:var(--accent)`），保留现有 `.nav-item.active::before` 的做法
4. **折叠态加 tooltip**：`el-tooltip` 包住图标，`:disabled="!collapsed"`，`placement="right"`
5. 底部新增用户卡：头像（首字母圆形）+ 用户名 + 角色标签（`MainLayout` 已有 `ROLE_LABELS` 映射，复用）+ 版本号
6. 保留 `.nav` 的 `min-height:0; overflow-y:auto`（**别删**，这是修过一次滚动裁切的 bug）
7. 分组标题小字化：11.5px / `--t3` / 字距 0.1em，与子项形成层级差

**禁止**：不动 `groups` 数组的 `to` / `label` / `perm`；不动 `isItemVisible` 权限过滤逻辑。

### 批次 3 · 顶栏（约 3 小时）

**文件**：新建 `src/components/AppTopBar.vue`，`src/layouts/MainLayout.vue` 瘦身

要点：
1. 从 `MainLayout.vue` 抽出顶栏为 `AppTopBar.vue`（props: `collapsed` / `alertCount` / `username` / `roleLabels`；emits: `toggle` / `refresh` / `logout` / `change-theme`）——**换肤逻辑 `applyTheme` / `changeTheme` 原样搬过去，一行别改**
2. 高度 `52px → 56px`
3. **新增 ⌘K 搜索入口**：一个 240px 的搜索框（不是真输入框，是按钮样式），显示 `搜索页面、主机…` + 右侧 `⌘K` 键帽，点击打开命令面板
4. **"数据大屏"入口降级**：删掉 `.screen-entry` 的紫渐变、`::before` shimmer、`::after` pulse、三个 keyframes；改为中性描边按钮（`border:1px solid var(--bd-strong)`，`background:transparent`，`color:var(--t2)`，hover 时 `--fill-hover`）
5. 图标按钮从 6 个精简到 4 个：折叠、刷新、告警（带 badge）、用户；主题切换并入用户下拉菜单
6. 面包屑保留，但字号降到 13px / `--t3`

**验收**：6 个入口功能全部可用；切换主题仍触发 `nebula:theme-changed`；无残留动画。

### 批次 4 · 命令面板（约 4 小时）

**文件**：新建 `src/components/CommandPalette.vue`，在 `MainLayout.vue` 挂载

要点：
1. `Ctrl/Cmd + K` 打开，`Esc` 关闭，点击遮罩关闭；`onMounted` 注册 `keydown`，`onUnmounted` 移除
2. 数据源**只从 `src/router/index.js` 的 routes 派生**（遍历 `router.options.routes` 取 `name` + `meta`，配合一份中文标题映射表），**不要手写第二份路由清单**（会漂移）
3. 分组显示：一级（概览）+ 各分组子项
4. 键盘：↑↓ 选择，Enter 跳转，`router.push` 后关闭
5. 空结果时显示 `EmptyState`
6. 样式：`--s3` 背景 + `--sh-3`，圆角 `--r-lg`，宽度 560px，顶部对齐 15vh

### 批次 5 · 首页概览（约 6 小时，本批最重）

**文件**：`src/components/OverviewView.vue`、`src/components/overview/{KpiBlock,HealthBlock}.vue`、新建 `overview/TriageBar.vue` 与 `src/composables/useTriage.js`

要点：
1. `OverviewView` 的 `.ov-header` 替换为 `PageHeader`（标题"系统概览"，描述沿用，操作区放"自定义布局/重置/刷新"）
2. `.ov-card` 圆角 14→10px，内边距改 16px，加 `.card` 的高光与阴影
3. **`KpiBlock` 去彩色图标**：`kpis` computed 里的 `icon` 保留，但四个卡片的图标底色统一为 `--fill-2` + `--t2` 图标色；**只有 `tone === 'bad'`（活跃告警有 critical）时数值才用 `--danger`**，其余用 `--t1`
4. **KPI 加迷你趋势**：每张卡数值下方加一条 36px 高的 sparkline（用 `vue-echarts` 的 `LineChart`，`grid` 全 0、`showSymbol:false`、线宽 1.5、`color` 跟随 `--accent`）。数据从现有 `latest.value.metrics` 派生，没有历史数据时用最近 N 个采样点，**不要新增接口**
5. **`HealthBlock`**：健康环升级——环宽 8px，中心数值 32px mono，下方四构成项（可用性/性能/容量/安全）用 `StatusPill` 展示
6. **新增 AI 研判条 `TriageBar.vue`**（置顶，在态势条之下）：
   - `src/composables/useTriage.js` 纯规则实现，**不接 LLM、不新增接口**，输入 `alerts` + `nodes` + `latestMap`，输出 `{ target, headline, reason, suggestion }`
   - 规则：按 `severity === 'critical'` 优先、其次 `startsAt` 最早，选中一条告警 → 根据告警名含"磁盘/内存/CPU/丢包/离线"匹配模板 → 拼出中文结论，例如"db-prod-01 磁盘使用率 91%，按当前增速约 6 小时后写满，建议清理归档表或扩容"
   - 视觉：左侧 `--danger` 竖条 + 结论文案 + 右侧"查看详情"按钮跳 `/alerts`
   - 无告警时整条不渲染
7. 保留 7 个 block 的可拖拽与显隐逻辑（`useOverviewLayout` 一行别动），只改外层皮肤

**验收**：自定义布局、排序、隐藏、重置四项功能与改前完全一致；三套主题下 sparkline 颜色跟随（**注意：不要把颜色硬编码成 `#3b82f6`，要用 `var(--accent)`**）。

### 批次 6 · 其余页面（约 4.5 天）

完整设计见 `design/pages-redesign.html`（六个 tab：列表页 / 详情页 / 告警中心 / 设置表单 / 四态与密度 / 页面清单）。
**不要逐页自由发挥**——按下面四套模板套，只有确实不合理的才做特例。

#### 6.0 先立两套全局规范（做页面之前）

**表格密度**（写进 `style.css` 的 `html.dark .el-table` 覆盖块，一次生效，惠及所有页面）：

| 项 | Element 默认 | 规范值 |
|---|---|---|
| 行高 | ≈48px | **38px** |
| 单元格字号 | 14px | 13px（`--fs-sm`） |
| 表头 | 14px 加粗 | 12px / 600 / `--t3` / 字距 .04em（**中文不要全大写**） |
| 斑马纹 stripe | 开 | **关**（深色下与 hover 底色互相干扰），只用 1px `--bd` 行线 |
| 行 hover | `--accent-dim` | `--fill-hover`；选中态另加左侧 2px 主色条 |
| 数值列 | 左对齐 / 比例字体 | **右对齐 + `font-variant-numeric:tabular-nums`** |
| 空态高度 | 60px | 160px（留给 EmptyState 的排查建议与动作） |

**四态统一**（现在各页面各写各的，这是当前最缺的一致性）：
- **空态**：一律 `EmptyState`。分两种文案——真无数据给「去添加/去配置」，筛选无结果给「清除筛选」并**回显当前筛选条件**
- **加载**：优先骨架屏，不用全屏 `v-loading` 遮罩（keep-alive 页面来回切会频繁闪遮罩）
- **错误**：标题 + 具体接口/原因 + 重试按钮，不要只留一行红字
- **部分失败**：多接口页面（如概览「KPI 成功但表格失败」）用顶部一条 `--warn` 提示条 + 「重试失败项」，**已成功的数据照常渲染**，不要整页报错

#### 6A · 高频列表页（约 1.5–2 天）
`HostsView` → `AlertsView` → `NodeView`（详情页，见 6B 模板）→ `AssetListView` → `OpsView` → `LogsView` → `SecurityView`

列表页模板（三处结构改动最值钱）：
1. **三层工具栏压成一层半**——现在 HostsView 是筛选条 + 刷新条 + Tab 条三张独立卡片，占约 160px，1080 屏只剩 11 行数据。改成：`PageHeader`（含主操作）+ 单行次级导航条（Tab 与刷新状态合并，Tab 用**下划线型**不用 `border-card`）+ 单行工具栏。**纵向省 60px**
2. 刷新倒计时用 14px 环形进度 + 秒数，落在次级导航条右端
3. 状态列 `el-tag effect="dark"` → `StatusPill`；主机列左侧加 3px 状态色条；hover 才出现的编辑按钮改为常驻弱化

告警中心另改三处：
- **KPI 只有「活跃告警」和「紧急」上红**，警告琥珀，信息/已抑制/24h 中性——颜色表达风险等级，不表达类别
- 维护窗口**关闭态折叠成一行**（现状无论开关都占一整块）
- 双击开对话框 → 改**右侧抽屉**（480px），列表保持可见可对照

#### 6B · 详情页与中间件（约 1.5 天）
`NodeView` → `MiddlewareView` → `redis/RedisTab`（1848 行 / 27 处硬编码白色，全站最重）→ `mysql` / `docker` Tab

模板：
1. **新增「对象头」区块**：20px 标题 + 状态 pill + mono 元信息；右侧 inline 四个关键指标（CPU/内存/磁盘/负载）。**不切 Tab 就知道这台机器什么状态**
2. Tab 统一为下划线型（弃用 `border-card`，深色下它与卡片是双层边框）
3. 设备信息从 8 项平铺网格改成**两列定义列表**：标签 12px `--t3` 固定 76px 左对齐，值 13px `--t1`
4. 仪表环外加 **80% / 90% 阈值刻度线**；副标题改有意义的信息（「剩余 178 GB」而不是「已用/总量」）
5. 端口状态改紧凑 `StatusPill` chips（mono 字号）

#### 6C · 设置与表单（约 1 天）
`settings/*` 四个子页 → `NotifyView` → `rbac/UsersView` / `RolesView` → `profile/ProfileView`

模板：
1. 横向 `el-tabs` → **左侧 200px 二级导航 + 右侧内容区**（子页内部还能再横分，不冲突）
2. 表单：标签右对齐固定 88px，控件最大宽 480px，分组用 `SectionCard`（标题 + 13px 副说明）
3. **底部 sticky 保存条，仅在有未保存改动时出现**，带未保存项数与「放弃」
4. 危险操作（重置/删除）单独用 `--danger-bd` 描边区块，与正常表单视觉分离，并说明影响范围
5. `RuleModal.vue`（617 行 / 21 个表单项平铺）改三步分步表单：条件 / 通知 / 生效范围

#### 6D · 图表与工具页（约 0.5 天）
`MetricsExploreView` → `DashboardView` → `DialTestView` → `ReportView`（外壳）

ECharts 主题统一读令牌（含 `--chart-*` 色板），换肤后重取色（见陷阱 4）。

#### 6E · 全局收口（约 0.5 天）
见 `design/pages-redesign.html` tab ⑤ 的 Element Plus 覆盖清单：`el-tag` 过渡期把 `effect="dark"` 改 `effect="plain"`（噪音立刻降一半）、`el-button` / `el-dialog` / `el-tabs` / `el-form` / `el-pagination` / `el-message` 的收敛项。

**三条纪律**：
1. **一次只改一个页面，改完即提交**——100 个组件一起改必然出现半吊子状态且难回滚
2. **先套模板，再做特例**（日志页需要密度切换这类才单独调）
3. **遇到硬编码白色顺手替换，不要专门开批次**：RedisTab 27、style.css 19、HostsView 10、SecurityView 9、NodeView 8

---

## 七、全局硬约束（每批都要遵守）

1. 每批结束 `npm run build` 必须通过
2. 不删除任何现有 CSS 变量名，只追加别名
3. 不改 `src/main.js:40` 的 `classList.add('dark')`（除非做附录 A）
4. 不改 `src/router/index.js` 的 `path` / `name` / `meta.perm`
5. 不改 `useOverviewLayout.js` / `useAuth.js` / `healthScore.js` 的任何逻辑
6. 新增文案全中文，不引入英文界面词
7. 新增组件放 `src/components/common/`，不散落
8. 每个批次一个 commit，commit message 写清批次号与改动范围

---

## 八、已知陷阱（踩过的坑，别重复）

| # | 陷阱 | 说明 |
|---|---|---|
| 1 | `.nav` 的 `min-height:0` | flex 子项默认 `min-height:auto` 不收缩，删了会导致菜单底部被裁且无法滚动。已修过一次 |
| 2 | `openGroups` 只在 `onMounted` 设一次 | 路由切换后分组不展开。批次 2 必须加 `watch(route.path)` |
| 3 | 硬编码白色 176 处 | 切主题时边框/填充不跟随，三套主题的"整体感"被稀释。这是本轮**顺带**要修的，不是主目标 |
| 4 | ECharts 取色 | 图表组件监听 `nebula:theme-changed` 事件重取色。新增 sparkline 也要监听，否则换肤后颜色不更新 |
| 5 | SVG 内 `font-family="var(--mono)"` | presentation attribute 不解析 `var()`，必须写真实字体名 |
| 6 | `--bg-card` 是 `rgba(...,.92)` | 半透明卡片叠在半透明上会越来越浑。新 `--s1` 是不透明色，替换时注意视觉差异 |
| 7 | `MainLayout` 的 `keep-alive :include` | 只缓存 `OverviewView`/`HostsView`/`AlertsView`，新增组件名不要误加进去 |
| 8 | `MainLayout` 用 `view.value.reload()` 刷新 | 顶栏抽出后这条链路必须保持，别改成事件总线 |
| 9 | 中文字距 | 中文在 ≥0.2em 字距下会散架，控制在 0.08–0.14em |
| 10 | 全项目零 `@media print` | 见附录 A，是真实功能缺陷 |

---

## 九、验收清单（全部打勾才算完成）

- [ ] `npm run build` 通过，无警告升级为错误
- [ ] 三套主题（极光蓝 / 星云青绿 / 星河紫）切换后：卡片、表格、按钮、图表、sparkline 颜色全部跟随
- [ ] 侧栏展开态：6 个分组默认全展开；切换路由后目标分组自动展开且高亮
- [ ] 侧栏折叠态：hover 显示中文 tooltip；点击分组能展开侧栏并定位
- [ ] 顶栏：无流光/脉冲动画残留；⌘K 入口可见可点
- [ ] 命令面板：⌘K 打开 → 输入 → ↑↓ → Enter 跳转 → Esc 关闭
- [ ] 首页：KPI 图标不再四色；有 critical 告警时 AI 研判条出现且文案正确；无告警时不渲染
- [ ] 首页自定义布局：排序 / 隐藏 / 重置三项行为与改前一致
- [ ] 抽查 5 个页面（`HostsView` `AlertsView` `NodeView` `SecurityView` `RedisTab`）无破版、无样式丢失
- [ ] 全站无新增英文界面文案

---

## 十、附录

### 附录 A · 打印样式层（可选，建议做，约半天）

**真实缺陷**：全项目 `@media print` 为零。主机清单、告警列表、资产台账这类页面用 Ctrl+P 时——开背景图形 = 满版黑底费墨，关背景图形 = 白纸浅字不可读，**两条路都是废纸**。

**做法**：新建 `src/assets/print.css`，在 `main.js` import。只在 `@media print` 内生效，屏幕渲染零影响：

```css
@media print {
  :root, body[data-theme] {
    --s0:#fff; --s1:#fff; --s2:#f5f7fa; --s3:#fff;
    --bd:#dcdfe6; --bd-strong:#c0c4cc;
    --t1:#1f2329; --t2:#4e5969; --t3:#86909c;
    --fill-1:#f5f7fa; --fill-2:#f5f7fa; --fill-3:#e5e6eb;
  }
  .sidebar, .topbar, .ov-actions, .el-pagination, button { display:none !important; }
  .card, .glass, .kpi, .ov-card, .panel {
    background:#fff !important; border:1px solid #dcdfe6 !important;
    box-shadow:none !important; break-inside:avoid;
  }
  thead { display:table-header-group; }   /* 跨页保留表头 */
  @page { margin:14mm; }
}
```

同时建议在页头操作区加一个"打印 / 存为 PDF"按钮（`window.print()`），避免用户自己去猜打印设置。

### 附录 B · 为什么不做全站浅色（如被追问，按此回答）

成本侧：45 个文件 176 处硬编码白色需逐个反转；`main.js` 强制 `dark`；Element Plus 只引 dark 变量；大屏的粒子/地图是深色专属资产。做完之后**每写一个新组件都要写两套**。

收益侧：这是值班盯屏、机房、夜间告警的平台，暗色是主场；团队已有三套主题说明差异化诉求在品牌色不在明暗。2026 的共识是 dark-first，不做浅色并不落后。

真正需要浅色的只有两处：附录 A 的打印层，以及对外 `/status` 页（若要给外部分享，需让 `main.js` 的 `add('dark')` 改为按路由 `meta.light` 条件切换——这是全站唯一有连带风险的地方，要单独隔离验证）。

**注意**：`src/components/StatusView.vue` 的样式本身就写成了浅色友好（`var(--border-color,#e5e7eb)` 带兜底值），是被 `main.js` 的全局 `dark` 拖黑的。所以这项改动量其实不大，约 1 天。

### 附录 C · 巡检报告打印（后端，非本轮范围）

`internal/server/report/template.html` 由 Go 模板 `//go:embed` 生成，前端 `ReportView.vue` 只是 iframe 预览。**该模板本身是浅色且已有 `@media print`**，但有三个缺陷：
1. hero 用 `linear-gradient(#2b5876,#4e4376)` + `color:#fff`——浏览器默认不打印背景图形，白字留白纸，**标题整块消失**（内容丢失，不是美观问题）
2. 缺 `thead{display:table-header-group}`，跨页后表头丢失
3. 无 `@page` 边距、页眉页脚、页码

修法约 2 小时，改后端模板，与本轮前端工作无耦合。
