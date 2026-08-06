## 用户需求

重新设计数据大屏功能，重构页面布局与数据分析能力，重点覆盖主机监控和中间件监控两个维度。主机监控需展示 CPU、内存、磁盘、网络等核心指标的趋势图与实时状态；中间件监控需支持常见组件（数据库、消息队列、缓存等）的健康度、连接数、响应时间等关键参数。针对 Nginx 数据实现专项分析模块，包括访问量统计、请求来源地理分布并在地图上以流量热力图或动线形式直观展示。整体大屏应以数据分析为核心，提供多维度图表（折线图、柱状图、仪表盘等）、数据下钻能力及异常告警提示，支持全屏自适应展示，配色与布局需突出数据可读性与视觉层次。

## 已确认决策

1. Nginx 地理分布数据源：Agent 解析 Nginx access log（真实数据），按 IP 聚合上报，Server 内置 IP 归属库映射地理。
2. 地图范围与形式：中国 + 世界双地图切换 + 热力散点/动线，GeoJSON 内置离线可用。
3. 移除现有中央数据中心拓扑图模块，专注数据分析（拓扑能力保留在中间件详情页）。

## 产品概述

深空科技风数据可视化大屏，单屏三板块（Tab 切换）：主机监控、中间件监控、Nginx 分析；顶部 KPI 行与底部异常告警滚动区恒显。支持全屏自适应、模块显隐配置持久化、图表点击下钻至详情页。

## 核心功能

- 主机监控板块：CPU/内存/磁盘/网络集群均值趋势折线图 + 实时仪表盘环图 + 主机健康列表（点击下钻 /node/:name）
- 中间件监控板块：8 类中间件（Redis/MySQL/PostgreSQL/Nginx/Kafka/Docker/RocketMQ/K8s）健康度总览（实例数/在线率/告警）+ 关键参数（连接数/响应时间/QPS/请求量，按类型 Tab 切换，点击下钻 /middleware）
- Nginx 分析板块：访问量/流量趋势折线、状态码分布柱状图、Top URI/Top IP 排行、请求来源地理分布（中国/世界双地图，热力散点 + 来源地到数据中心动线）
- 异常告警：底部实时告警滚动列表 + 告警脉冲提示 + KPI 异常着色
- 顶部 KPI：服务器总数/在线主机/平均 CPU/平均内存/中间件实例数/异常告警数/实时流量
- 全屏自适应：1920x1080 基准 grid 布局 + 响应式断点，全屏按钮
- 模块显隐设置：沿用现有设置抽屉，配置持久化到后端（/api/v1/screen/config）

[object Object]```

### 数据下钻链路

```mermaid[object Object]```

### 关键设计决策

1. Access log 采集（Agent 端）：新增独立采集器 NginxAccessCollector，在 Collector 挂载（新增 CollectNginxAccess()，与 CollectNginx 并列，由 agent 主流程填入 payload）。内存记录文件 offset 做增量 tail；解析 combined 格式（$remote_addr/$time_local/$request/$status/$body_bytes_sent，支持可选 $request_time）。每上报周期聚合：按 IP 统计请求数/流量/状态码（仅上报 Top-N=200 控制时序库基数）、状态码分布、Top URI 10。
2. 配置扩展：NginxInstanceConfig 新增 AccessLog（日志文件路径，留空不采集）与 LogFormat（默认 combined）字段，agent.yaml 向后兼容。
3. Server 端地理聚合：新增 internal/server/nginxaccess 包。geo.go 封装 ip2region（embed xdb）；window.go 内存滑动窗口，key=instance|省/国家，聚合 requests/bytes，TTL 1h 定期清理（实时大屏场景接受重启丢数据，不做历史落盘）。两级基数控制：Agent 先 IP Top-N，Server 再聚合到地理维度（省 34 + 国家约 200，内存安全）。
4. 中间件健康度总览：后端新增 GET /api/v1/middleware/overview，一次性 QueryAllLatest 各 instance_up 指标 + 告警过滤聚合（避免前端 8 请求轮询），返回各类型 total/up/down/alertCount。关键参数趋势复用现有 instances API 与 /api/v1/query/range（redis_connected_clients、redis_cmd_latency_ms、mysql_query_latency_ms、postgres_query_latency_ms、nginx_active_connections 等）。
5. 主机监控数据：复用现有 /api/v1/nodes/latest（cpu/mem/disk/load1/netIn/netOut/diskRead/diskWr）与 /api/v1/query/range 集群聚合（沿用现有 aggregate() 与 sample.slice(0,20) 并发限制先例）。
6. 大屏布局：移除 TopologyMap.vue 引用（文件保留不删），新布局 grid-template-areas：top(标题栏)/kpi(KPI 行)/main(Tab 主区)/bottom(告警滚动区)。主区三 Tab。设置抽屉模块 key 更新为 kpiTop/hostMonitor/middlewareMonitor/nginxAnalysis/alerts 等，DefaultScreenConfig 同步扩展（internal/server/config/config.go）。
7. 性能：沿用现有 30s 轮询 + WS(alerts) 刷新、gradientCache 渐变复用、图表 dispose/resize；地图动线数据为省/国家级，量小无性能风险；access log 解析只读增量行，聚合为 O(行数)。

## 实施要点

- 版本号单一来源 VERSION（1.15.0 升 1.16.0）；编译必须走 build/cross-compile.sh（读 VERSION 经 ldflags 注入，输出 6 个二进制）；前端改动后 cd web && npm install && npm run build。
- Agent 端采集/上报改动必须与 Server 同步升级并重分发 agent 二进制。
- README.md 面向终端用户：新增 Nginx access log 配置说明（log_format 配置示例 + agent.yaml accessLog 路径），使用中性操作式语言，避免内部决策口吻。
- ip2region 依赖需 GOPROXY=https://goproxy.cn,direct；xdb 文件下载放入 internal/server/nginxaccess/data/ 并用 go:embed 嵌入。
- GeoJSON 需一次性下载（阿里云 DataV GeoAtlas / echarts 地图数据仓库，含南海诸岛与国界完整版）放入 web/src/assets/geo/。
- 每次代码修改后 git 提交，提交前检查工作区避免混入无关改动。

## 目录结构

```
nebula-monitor/
├── internal/
│   ├── model/
│   │   └── metric.go                 # [MODIFY] 新增 NginxAccessStat 结构；NginxInstanceConfig 加 AccessLog/LogFormat；ReportPayload 加 NginxAccessStats
│   ├── agent/
│   │   ├── collector/
│   │   │   ├── nginx_access.go       # [NEW] access log 增量 tail + combined 解析 + IP/URI/状态码聚合采集器
│   │   │   └── collector.go          # [MODIFY] 挂载 NginxAccessCollector，新增 CollectNginxAccess() 方法
│   │   └── config/
│   │       └── config.go             # [MODIFY] CollectorToggle 增加 nginx 日志开关；nginx 实例配置透传
│   ├── server/
│   │   ├── nginxaccess/              # [NEW] 地理分布服务
│   │   │   ├── geo.go                # ip2region 封装（embed xdb，SearchByStr 到 国家/省/市）
│   │   │   ├── window.go             # 内存滑动窗口（instance|省|国家 到 requests/bytes，TTL 1h）
│   │   │   └── data/ip2region.xdb    # [NEW] IP 归属库（约 11MB，go:embed）
│   │   ├── receiver/
│   │   │   └── receiver.go           # [MODIFY] 处理 NginxAccessStats：写 VM 低基数指标 + 送地理窗口
│   │   ├── api/
│   │   │   ├── nginx_access.go       # [NEW] GET /api/v1/middleware/nginx/access/summary、/access/geo
│   │   │   ├── query.go              # [MODIFY] 注册新路由
│   │   │   └── middleware_api.go     # [MODIFY] 新增 handleMiddlewareOverview 健康度总览
│   │   └── config/
│   │       └── config.go             # [MODIFY] DefaultScreenConfig 模块 key 更新（去 topology，加 hostMonitor/middlewareMonitor/nginxAnalysis）
│   └── cmd/agent 主流程              # [MODIFY] 组装 ReportPayload 时调用 CollectNginxAccess()
├── web/
│   ├── src/
│   │   ├── assets/geo/
│   │   │   ├── china.json            # [NEW] 中国省份 GeoJSON（含南海诸岛）
│   │   │   └── world.json            # [NEW] 世界国家 GeoJSON
│   │   ├── charts/
│   │   │   └── echarts.js            # [MODIFY] 新增地图注册与 mapGeoOption（热力散点+动线）工厂
│   │   └── components/screen/
│   │       ├── ScreenView.vue        # [MODIFY] 全新布局：顶栏+KPI+三Tab主区+底部告警；移除 TopologyMap 引用
│   │       ├── HostMonitorPanel.vue  # [NEW] 主机板块（4仪表盘+4趋势图+主机健康列表）
│   │       ├── MiddlewareMonitorPanel.vue  # [NEW] 中间件板块（健康度总览+关键参数Tab+告警下钻）
│   │       ├── NginxAnalysisPanel.vue      # [NEW] Nginx 板块（访问量趋势+状态码分布+Top排行）
│   │       ├── GeoMap.vue            # [NEW] 地理地图（中国/世界切换、热力散点、动线）
│   │       ├── ScreenTrend.vue       # [保留复用] 迷你趋势图
│   │       ├── ScreenGauges.vue      # [改造复用] 仪表盘环图
│   │       └── TopologyMap.vue       # [不再引用，文件保留]
├── VERSION                           # [MODIFY] 1.15.0 升 1.16.0
├── go.mod                            # [MODIFY] 加 ip2region 依赖
└── README.md                         # [MODIFY] Nginx access log 配置说明（终端用户语言）
```

## 关键代码结构

```
// internal/model/metric.go 新增：周期内 Nginx access log 聚合统计（每实例一条）
type NginxAccessStat struct {
	Instance    string             `json:"instance"`              // nginx 实例地址（与 nginx_instance_up 对齐）
	Group       string             `json:"group"`
	PeriodSec   float64            `json:"periodSec"`             // 聚合周期秒数（用于速率）
	Requests    float64            `json:"requests"`              // 周期内请求数
	Bytes       float64            `json:"bytes"`                 // 周期内响应字节
	AvgLatency  float64            `json:"avgLatency,omitempty"`  // 平均响应时间 ms（日志含 request_time 时）
	StatusCount map[string]float64 `json:"statusCount"`           // 状态码分布 {200: 123, 404: 5}
	TopURIs     []NameCount        `json:"topUris,omitempty"`     // Top 10 URI
	TopIPs      []IPCount          `json:"topIps,omitempty"`      // Top-N IP
}
type IPCount struct{ IP string `json:"ip"`; Requests float64 `json:"requests"`; Bytes float64 `json:"bytes"` }
type NameCount struct{ Name string `json:"name"`; Count float64 `json:"count"` }
// ReportPayload 增加：NginxAccessStats []NginxAccessStat `json:"nginxAccessStats,omitempty"`

// API 响应结构（internal/server/api/nginx_access.go）
// GET /api/v1/middleware/nginx/access/summary 返回：
// { instances:[{instance,node,nodeIp,requests,bytes,reqRate,activeConnections,up}],
//   totalRequests, totalRate, statusCounts:{200:..,404:..},
//   topUris:[{name,count}], topIps:[{ip,requests,bytes,country,province}] }
// GET /api/v1/middleware/nginx/access/geo?scope=cn|world 返回：
// { points:[{name,value,geo:[lng,lat],country}],        // 来源地热力（省/国家）
//   deployPoints:[{name,geo:[lng,lat]}],                // 数据中心部署点（node IP 归属，内网回退地图中心）
//   lines:[{from:[lng,lat],to:[lng,lat],value}] }        // 动线（来源 到 部署点）
```

]]>

## 设计风格

深空科技风数据大屏（dark cyber data-screen）：深蓝黑渐变背景叠加深海网格与微弱光晕，玻璃拟态半透明面板配霓虹渐变描边与 HUD 装饰角标，图表以发光渐变面积线、环状仪表盘、高饱和色阶呈现，营造专业、沉浸、高可读的运维指挥中心质感。延续现有蓝青色系（--info/--accent/--violet），与全局主题一致。

## 页面规划（单屏，Tab 切换三大板块）

- 顶部标题栏：居中发光渐变标题 + 左侧实时时钟 + 右侧全屏/设置/返回按钮；两侧 HUD 装饰线
- KPI 横排：8 张玻璃卡片（服务器总数/在线主机/平均 CPU/平均内存/中间件实例/异常告警/实时流量），等宽字体大字，异常项红色高亮加脉冲
- Tab 主区（核心）：三个板块 Tab 切换，滑块渐变动画
- 主机监控板块：左侧 4 个圆环仪表盘（CPU/内存/磁盘/网络）+ 主机健康表格；右侧 2x2 趋势折线图区
- 中间件监控板块：上部 8 类型健康度总览卡片（实例数/在线率环形进度/告警角标）；下部左侧关键参数趋势图、右侧实例列表与告警
- Nginx 分析板块：左侧访问量趋势折线 + 状态码分布柱状 + Top IP/URI 双排行；右侧大屏地图（中国/世界切换按钮、热力散点呼吸动效、来源到数据中心动线流动动画）+ 来源 Top 列表
- 底部告警滚动区：横向滚动条展示最新异常告警（级别色点+设备+时间），critical 红色脉冲

## 交互细节

- 图表 hover 高亮 + 深色玻璃 tooltip；地图散点涟漪动画，动线粒子流动
- 卡片 hover 微浮起加边框发光；KPI 数值变化平滑过渡
- 数据下钻：主机行/中间件实例/告警行点击跳转详情页
- 每 30s 数据轮询 + WS 告警即时刷新，页面隐藏时暂停轮询（沿用现有可见性优化）
- 全屏自适应：grid 基准 1920x1080，@media 断点收缩列宽与字号，图表随容器 resize

## Agent Extensions

### Skill

- **frontend-design**
- 用途：用于大屏前端 UI 重构（ScreenView 新布局、三大板块组件、GeoMap 地图组件），保证科技风大屏的视觉质量与交互细节达到生产级水准
- 预期产出：高质量、可运行的大屏界面代码，配色/布局/微动效符合数据可视化设计规范