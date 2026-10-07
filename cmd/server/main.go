// Command server 是中心服务端：接收上报、写 VM、提供 API/仪表盘与告警。
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/nebula/monitor/internal/server/agentdist"
	"github.com/nebula/monitor/internal/server/alert"
	"github.com/nebula/monitor/internal/server/analysis"
	"github.com/nebula/monitor/internal/server/api"
	"github.com/nebula/monitor/internal/server/asset"
	"github.com/nebula/monitor/internal/server/audit"
	"github.com/nebula/monitor/internal/server/auth"
	"github.com/nebula/monitor/internal/server/config"
	servercrypto "github.com/nebula/monitor/internal/server/crypto"
	"github.com/nebula/monitor/internal/server/dashboard"
	"github.com/nebula/monitor/internal/server/dialtest"
	"github.com/nebula/monitor/internal/server/instancereg"
	"github.com/nebula/monitor/internal/server/logstore"
	"github.com/nebula/monitor/internal/server/mwreg"
	"github.com/nebula/monitor/internal/server/mwview"
	"github.com/nebula/monitor/internal/server/nginxaccess"
	"github.com/nebula/monitor/internal/server/node"
	"github.com/nebula/monitor/internal/server/notify"
	"github.com/nebula/monitor/internal/server/ops"
	"github.com/nebula/monitor/internal/server/receiver"
	"github.com/nebula/monitor/internal/server/report"
	"github.com/nebula/monitor/internal/server/retention"
	"github.com/nebula/monitor/internal/server/screencfg"
	"github.com/nebula/monitor/internal/server/security"
	"github.com/nebula/monitor/internal/server/selfmon"
	"github.com/nebula/monitor/internal/server/storage"
	"github.com/nebula/monitor/internal/server/uicfg"
	"github.com/nebula/monitor/internal/server/upgrade"
	"github.com/nebula/monitor/internal/version"
)

func main() {
	cfgPath := flag.String("config", "server.yaml", "配置文件路径")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		slog.Error("加载配置失败", "err", err)
		os.Exit(1)
	}

	// 登录密码平滑迁移：若配置中仍为明文，则自动转为 bcrypt 哈希并写回配置文件。
	if migrated, merr := cfg.Auth.MigratePasswordIfNeeded(*cfgPath); merr != nil {
		slog.Warn("登录密码迁移为哈希失败（不影响启动，下次登录将按明文兜底）", "err", merr)
	} else if migrated {
		slog.Info("登录密码已自动迁移为 bcrypt 哈希", "path", *cfgPath)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	// signalCtx 只负责通知「该退出了」；后台任务用独立的 runCtx，
	// 这样信号一到不会立刻掐断仍由 Shutdown 宽限期服务的在途请求。
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()

	// 存储层
	store, err := storage.NewStorage(cfg.TSDB)
	if err != nil {
		slog.Error("初始化时序库失败", "err", err)
		os.Exit(1)
	}
	// 自监控：包装存储后，Server 侧全部 TSDB 读写自动计数（装饰器，无需改动各调用点）。
	// rawStore 保留未包装引用，供自监控自身上报使用，免得把自己的写入也统计进去。
	rawStore := store
	mon := selfmon.New(version.Version)
	store = selfmon.NewStorage(rawStore, mon)

	// 节点管理
	nodeMgr := node.New(cfg.NodeMeta, time.Duration(cfg.OfflineTimeout)*time.Second)

	// Agent 接入授权（参考哪吒探针：启用后 Agent 需携带密钥；未配置则启动自动生成随机密钥）
	if cfg.AgentAuth.Enabled && cfg.AgentAuth.Secret == "" {
		cfg.AgentAuth.Secret = genSecret()
		slog.Warn("agentAuth.secret 未配置，已自动生成随机密钥（重启会变，建议写入配置固定）",
			"secret", cfg.AgentAuth.Secret)
	}
	// 登录认证：启用且 secret 为空时自动生成（重启会失效，建议写入配置固定）
	if cfg.Auth.Enabled && cfg.Auth.Secret == "" {
		cfg.Auth.Secret = genSecret()
		slog.Warn("auth.secret 未配置，已自动生成随机密钥（重启后登录失效，建议写入配置固定）")
	}

	// 多用户角色权限数据存储：默认 <DataDir>/users.yaml。
	usersFile := cfg.Auth.UsersFile
	if usersFile == "" {
		usersFile = filepath.Join(cfg.DataDir, "users.yaml")
	}
	var authStore *auth.Store
	if cfg.Auth.Enabled {
		st, err := auth.NewStore(usersFile)
		if err != nil {
			slog.Error("初始化用户权限存储失败", "err", err)
			os.Exit(1)
		}
		// 首次启动将单管理员账号幂等迁移为超级管理员。
		if cfg.Auth.MigrateSingleAdmin {
			if _, merr := auth.MigrateSingleAdmin(st, cfg.Auth.Username, cfg.Auth.Password, servercrypto.IsHashed(cfg.Auth.Password)); merr != nil {
				slog.Warn("单管理员迁移失败", "err", merr)
			}
		}
		authStore = st
	}

	// 告警相关
	rules := alert.NewRulesStore(cfg.Alert.RulesFile)
	alertStore := alert.NewVMAlertStore(store)
	ackStore := alert.NewAckStore(filepath.Join(filepath.Dir(cfg.Alert.RulesFile), "alert_acks.json"))
	maintenance := alert.NewMaintenanceStore(cfg.Alert.MaintenanceFile)
	// 抑制规则与分组配置（P4）：热加载 YAML，可经 Web 端增删改。
	inhibitStore := alert.NewInhibitStore(filepath.Join(filepath.Dir(cfg.Alert.RulesFile), "alert_inhibit.yaml"))
	groupingStore := alert.NewGroupingStore(filepath.Join(filepath.Dir(cfg.Alert.RulesFile), "alert_grouping.yaml"))
	// 告警事件管道（relabel / enrich / 消息模板）：独立 YAML，Web 端可编辑，保存即热生效。
	pipelineStore := alert.NewPipelineStore(filepath.Join(filepath.Dir(cfg.Alert.RulesFile), "alert_pipeline.yaml"))
	// 通知器统一经自监控装饰器包装：按渠道统计发送成功/失败（含配置热加载路径）
	buildNotifiers := func(c config.NotifyConfig) []alert.Notifier {
		return selfmon.WrapNotifiers(alert.BuildNotifiers(c), mon)
	}
	notifiers := buildNotifiers(cfg.Notify)
	hub := api.NewHub()
	engine := alert.NewEngine(store, nodeMgr, rules, alertStore, notifiers, hub, maintenance, cfg.Alert.EvalInterval, inhibitStore, groupingStore, pipelineStore)

	// 通知配置管理：独立文件（Web 端可配置），启动时优先加载该文件，不存在则
	// 用 server.yaml 的 notify 段初始化并落盘；保存时通过 SetNotifiers 热加载。
	notifyMgr, err := notify.New(cfg.NotifyFile, cfg.Notify, func(c config.NotifyConfig) {
		engine.SetNotifiers(buildNotifiers(c))
	})
	if err != nil {
		slog.Error("初始化通知配置失败", "err", err)
		os.Exit(1)
	}
	// 用加载后的配置同步内存通知器（若文件存在则覆盖初始 cfg.Notify）。
	engine.SetNotifiers(buildNotifiers(notifyMgr.Get()))

	// 自监控采集点接在「已有的真实状态」上（拉取式），避免另记一份而漂移。
	mon.SetWSStats(hub.ClientCount)
	mon.SetAlertStats(engine.ActiveCounts)
	mon.SetNodeStats(func() (online, total int) {
		nodes := nodeMgr.ListNodes()
		for _, n := range nodes {
			if n.Status == "online" {
				online++
			}
		}
		return online, len(nodes)
	})
	// 仅在启用告警时注入评估节拍，否则 /readyz 会把「未启用」误判为「评估停摆」。
	if cfg.Alert.Enabled {
		mon.SetEvalInterval(time.Duration(cfg.Alert.EvalInterval) * time.Second)
		engine.SetEvalObserver(mon.AddEval)
	}

	// IP 地理库：优先加载磁盘上的覆盖文件（Web 端「IP 地理库」入口上传更新），
	// 文件缺失或损坏时回退到随程序内置的库，不影响启动。
	if err := nginxaccess.SetGeoOverridePath(cfg.GeoIPFile); err != nil {
		slog.Warn("加载 IP 地理库覆盖文件失败，已回退内置库", "path", cfg.GeoIPFile, "err", err)
	}

	// 中间件监控页面展示开关（哪些类型出现在 Tab 上）：空清单 = 全部展示
	mwview.Default.SetPath(cfg.MiddlewareViewFile)

	// 上报接收（Nginx access log 地理聚合窗口：TTL 1h，实时大屏场景）
	ngxWin := nginxaccess.NewWindow(nginxaccess.NewGeo(), time.Hour)
	// 安全事件/基线存储（JSON 持久化，默认安全能力启用）
	securityStore := security.New(cfg.SecurityStoreFile)
	defenseStore := security.NewDefenseStore(filepath.Join(filepath.Dir(cfg.SecurityStoreFile), "defense_tasks.json"))
	recv := receiver.New(store, nodeMgr, cfg.AgentAuth, ngxWin, securityStore, engine, defenseStore)

	// 统一下行操作通道：任务落盘在数据目录（与防护任务同处），receiver 用它领取/回执/回收，
	// API 用它创建与查询。两端都注入同一份实例——分开注入会得到"创建了却永远不下发"的哑功能。
	opsDataDir := filepath.Dir(cfg.SecurityStoreFile)
	// 单独取一个变量：台账库打开后要把这个存储切到入库模式（见下方 assetStore 分支）。
	opsStore := ops.NewStore(filepath.Join(opsDataDir, "ops_tasks.json"))
	opsSvc := ops.NewService(opsStore)
	// 文件分发的内容存储（ops_files/）：内容与任务分开存，任务里只留引用，
	// 否则 ops_tasks.json 会以「条数 × 文件大小」的量级膨胀（见 ops/files.go 的说明）。
	// 打不开时只记一条并继续：受影响的是文件分发（创建任务时会明确报错），
	// 其余下行动作不受影响——不该因为它让整个服务起不来。
	if opsFiles, err := ops.OpenFileStore(filepath.Join(opsDataDir, "ops_files")); err != nil {
		slog.Error("操作文件存储初始化失败，文件分发不可用", "err", err)
	} else {
		opsSvc.SetFileStore(opsFiles)
	}
	recv.SetOps(opsSvc)

	// 集中日志（C2）：目录留空时取 <DataDir>/logs；未配置时该能力关闭（接口回 503）。
	// 三个上限都必须有值——「开了日志把盘写满」不是会不会的问题，只是时间问题。
	logDir := cfg.LogDir
	if logDir == "" {
		logDir = filepath.Join(cfg.DataDir, "logs")
	}
	// 日志后端可替换（ADR-0002）：默认自研分片落盘，可选外部后端 VictoriaLogs。
	// 后端名写错**必须让启动失败**：静默降级成"日志不可用"会把一个配置笔误
	// 变成一次"日志功能消失了"的事故（而日志恰好是排障时才去看的东西）。
	logBackend, err := logstore.NewBackend(cfg.LogBackend, logstore.BackendOptions{
		Dir:            logDir,
		MaxBytesPerDay: cfg.LogMaxBytesPerDay,
		VictoriaLogs: logstore.VictoriaLogsOptions{
			Addr:         cfg.LogVictoriaLogs.Addr,
			QueryTimeout: time.Duration(cfg.LogVictoriaLogs.QueryTimeout) * time.Second,
			WriteTimeout: time.Duration(cfg.LogVictoriaLogs.WriteTimeout) * time.Second,
		},
	})
	if err != nil {
		slog.Error("初始化集中日志后端失败", "backend", cfg.LogBackend, "err", err)
		os.Exit(1)
	}
	// 同一个实例既供上行写入、也供检索读取：读写两侧共用同一个后端实例，
	// 不会出现"写到一个后端、读另一个后端"这种只在查不到日志时才暴露的问题。
	// logBackend 是接口：nil 表示未启用（上面已把"具体类型的 nil"挡在工厂里）。
	recv.SetLogStore(logBackend, cfg.LogMaxBodyBytes, cfg.LogUploadRateBps)
	if logBackend != nil {
		slog.Info("集中日志已启用", "backend", logBackend.Backend(), "dir", logDir)
	}

	// 资产台账（内嵌 SQLite 单文件）：库路径留空时取 <DataDir>/assets.db。
	// 打开失败**不阻断启动**——台账是次要能力，缺它时监控主链路仍应可用。
	var assetSvc *asset.Service
	// 周期化巡检调度（设计件 2026-10-07-inspect-schedule-design.md）：构造在资产服务之后
	// （它要调 RunInspect），启动在下面的后台任务块里（与报告调度同一位置）。
	var inspectSched *asset.InspectScheduler
	assetPath := cfg.AssetStoreFile
	if assetPath == "" {
		assetPath = filepath.Join(cfg.DataDir, "assets.db")
	}
	// 库句柄要在**审计与告警处置**之后还要用（它们与台账共用同一个库），故提到外层作用域。
	var assetStore *asset.Store
	if s, err := asset.Open(assetPath); err != nil {
		slog.Error("资产台账库不可用，资产能力已关闭", "path", assetPath, "err", err)
	} else {
		assetStore = s
		defer func() { _ = s.Close() }()
		assetSvc = asset.NewService(assetStore)
		recv.SetAssetService(assetSvc)
		slog.Info("资产台账已启用", "path", assetPath)
		// 告警处置与台账**共用同一个库**（设计件批次 18）：写入频率是人工操作级
		// （每分钟几条到几十条），单连接下这点增量可忽略，换来一个备份文件、一套迁移机制。
		// 首次启动会把既有 alert_acks.json 回填进库并改名 .bak-migrated。
		// 回填失败**不阻断启动**：记日志，继续按 JSON 降级模式工作。
		if err := ackStore.UseSQLite(assetStore.DB()); err != nil {
			slog.Error("告警处置改用数据库失败，继续按 JSON 文件工作", "err", err)
		}
		// 操作任务同理（设计件批次 19）。首次启动会把既有 ops_tasks.json 回填进库并改名
		// .bak-migrated；回填失败不阻断启动，继续按 JSON 降级模式工作。
		if err := opsStore.UseSQLite(assetStore.DB()); err != nil {
			slog.Error("操作任务改用数据库失败，继续按 JSON 文件工作", "err", err)
		}
	}
	// 中间件类型注册表：内置类型清单的唯一来源
	//（api 的类型清单、报告分节、告警的服务类型校验都读它）。
	mwRegistry := mwreg.New()

	// 拨测模块
	dialtestStore := dialtest.NewStore(cfg.DialtestFile)
	dialtestSched := dialtest.NewScheduler(dialtestStore, store)
	// 拨测状态跃迁联动告警事件（P2）：故障/恢复统一进入告警中心并通知。
	if cfg.Alert.Enabled {
		dialtestSched.SetSink(engine)
	}
	dialtestSched.Start(runCtx)

	// 智能分析模块：仅查询既有时序数据，不影响上报与告警评估链路。
	analyzer := analysis.New(store, nodeMgr)
	analyzer.SetAlertStore(alertStore)
	analyzer.SetSecurityStore(securityStore)
	analyzer.SetInstanceRegistry(instancereg.Default)
	analyzer.SetDialtestStore(dialtestStore)

	// 报告生成模块
	reportGen := report.NewGenerator(store, nodeMgr, securityStore, cfg.ReportDir)
	reportGen.SetAnalyzer(analyzer)
	// 报告的分节同样来自注册表：模板派生类型会作为独立一节出现在报告里
	reportGen.SetMiddlewareRegistry(mwRegistry)
	// 报告周期化调度：默认**关闭**（要人显式打开），配置文件不存在时用默认值建立。
	// 建不起来（如目录不可写）不阻断启动：报告仍可在 Web 端手动生成。
	var reportSched *report.Scheduler
	if sched, err := report.NewScheduler(filepath.Join(cfg.DataDir, "report_schedule.yaml"),
		report.ScheduleConfig{Enabled: false, Type: string(report.ReportWeekly), IntervalHours: report.DefaultScheduleIntervalHours},
		reportGen); err != nil {
		slog.Error("初始化报告调度失败（周期化报告不可用，手动生成不受影响）", "err", err)
	} else {
		reportSched = sched
	}

	// 数据大屏模块显隐配置管理：独立文件（Web 端设置写入），不存在则用默认全开初始化并落盘。
	screenMgr, err := screencfg.New(cfg.ScreenFile, config.DefaultScreenConfig())
	if err != nil {
		slog.Error("初始化大屏配置失败", "err", err)
		os.Exit(1)
	}

	// 系统 UI 品牌配置（系统名称/Logo）：独立文件（Web 端设置写入），不存在则用默认初始化并落盘。
	uiMgr, err := uicfg.New(cfg.UIFile, uicfg.DefaultUIConfig())
	if err != nil {
		slog.Error("初始化 UI 配置失败", "err", err)
		os.Exit(1)
	}

	// 自定义仪表盘配置（文件缺失则初始化为空，不阻断启动）
	dashMgr, err := dashboard.New(cfg.DashboardsFile)
	if err != nil {
		slog.Error("初始化仪表盘配置失败", "err", err)
		os.Exit(1)
	}

	// 系统升级管理器
	var upgrader *upgrade.Manager
	if cfg.Upgrade.Enabled {
		upgrader, err = upgrade.New(upgrade.Config{
			Dir:              cfg.Upgrade.Dir,
			BinDir:           cfg.Upgrade.BinDir,
			WebDir:           cfg.WebDir,
			AgentBinDir:      cfg.AgentBinDir,
			AgentScriptPath:  cfg.AgentScriptPath,
			BackupKeep:       cfg.Upgrade.BackupKeep,
			ArchiveKeep:      cfg.Upgrade.ArchiveKeep,
			UseSystemd:       cfg.Upgrade.UseSystemd,
			Service:          cfg.Upgrade.Service,
			CurrentVersion:   version.Version,
			ServerConfigPath: *cfgPath,
			ExtraConfigPaths: []string{
				cfg.NotifyFile,
				cfg.DialtestFile,
				cfg.ScreenFile,
				cfg.UIFile,
				cfg.GeoIPFile,
				filepath.Join(filepath.Dir(*cfgPath), "audit_events.json"),
			},
		}, nodeMgr)
		if err != nil {
			slog.Error("初始化升级管理器失败", "err", err)
			os.Exit(1)
		}
	}

	// API
	auditStore := audit.New(filepath.Join(filepath.Dir(*cfgPath), "audit_events.json"))
	// 审计与台账**共用同一个库**（设计件批次 18）。这里才注入是因为审计存储在本行才创建。
	// 首次启动会把既有 audit_events.json 回填进库并改名 .bak-migrated（幂等：判据是表为空）。
	// 台账库不可用（assetStore == nil）时保持 JSON 降级模式，审计不跟着失效。
	if assetStore != nil {
		if err := auditStore.UseSQLite(assetStore.DB()); err != nil {
			slog.Error("审计事件改用数据库失败，继续按 JSON 文件工作", "err", err)
		}
	}
	// 数据保留策略：清理本地可清理的数据（告警处置记录、巡检报告），
	// 并只读呈现时序库保留期与内置上限的现状。
	retentionFile := cfg.RetentionFile
	if retentionFile == "" {
		retentionFile = filepath.Join(cfg.DataDir, "retention.yaml")
	}
	retentionMgr, err := retention.New(retentionFile, retention.DefaultConfig(), ackStore, reportGen, auditStore, securityStore, cfg.TSDB.Addr)
	if err != nil {
		slog.Error("初始化数据保留策略失败", "err", err)
		os.Exit(1)
	}
	// 集中日志的保留清理只对**自研落盘**生效：按「来源/日期/节点」的日期分片整天删除。
	// 外部后端的保留期由它自己管（VictoriaLogs 的 -retentionPeriod），平台既不该也不能
	// 去删它的数据；但必须显式声明，否则界面会把"日志 0 文件"说成"日志未接入"。
	if local, ok := logBackend.(*logstore.Store); ok {
		retentionMgr.SetLogStore(local)
	} else if logBackend != nil {
		retentionMgr.SetLogStoreExternal(logBackend.Backend())
	}
	rest := api.New(store, nodeMgr, rules, alertStore, hub, cfg.AgentAuth, cfg.AgentBinDir, cfg.WebDir, cfg.Auth, upgrader, notifyMgr, engine, maintenance, dialtestStore, reportGen, screenMgr, ackStore, inhibitStore, groupingStore, ngxWin, uiMgr, *cfgPath, securityStore, defenseStore, auditStore, authStore)
	rest.SetDashboardManager(dashMgr)
	// 下行操作通道：与 receiver 共用同一个 service 实例（见上面 opsSvc 的说明）
	rest.SetOpsService(opsSvc)
	rest.SetAnalyzer(analyzer)
	rest.SetPipelineStore(pipelineStore)
	rest.SetSelfMon(mon)
	rest.SetRetention(retentionMgr)
	// 报告周期化调度：仅在调度器建起来时注入（未注入时接口回 503，而不是假装能用）
	if reportSched != nil {
		rest.SetReportScheduler(reportSched)
	}
	// 集中日志检索（C2）：与上行共用同一个后端实例（读写两侧必然是同一个后端）
	rest.SetLogStore(logBackend)
	rest.SetMiddlewareRegistry(mwRegistry)
	// 资产台账接口：仅在库可用时注入（传 nil 具体值进接口会得到「非 nil 接口」，必须显式判断）
	if assetSvc != nil {
		rest.SetAssetService(assetSvc)
		// 周期化巡检：配置与运行状态同文件；范围折算由 API 层提供（它持有身份仓库与节点管理器，
		// 而 asset 包刻意不依赖 auth 包，依赖方向保持清晰）。
		// 构造失败**不阻断启动**：它是次要能力，缺它时巡检仍可手动触发。
		if s, err := asset.NewInspectScheduler(filepath.Join(cfg.DataDir, "inspect_schedule.yaml"),
			asset.InspectScheduleConfig{}, assetSvc, rest.InspectScopeSnapshot); err != nil {
			slog.Error("周期化巡检配置不可用，该能力已关闭", "err", err)
		} else {
			inspectSched = s
			rest.SetInspectScheduler(s)
		}
	}
	// 告警侧同样读注册表：服务离线规则的服务类型校验都读它
	alert.SetMiddlewareRegistry(mwRegistry)
	mux := http.NewServeMux()
	recvMux := &receiverMux{recv: recv}
	recvMux.register(mux)
	rest.RegisterRoutes(mux)
	rest.RegisterDashboard(mux)
	// WebSocket 端点经 API 注册：在握手前完成 topic 级授权与节点资源范围校验。
	rest.RegisterWS(mux, store)

	// Agent 分发（自带 CDN：安装脚本 + 各架构二进制）
	agentdist.New(cfg.AgentBinDir, cfg.AgentScriptPath, cfg.AgentAuth).Register(mux)

	// 启动后台任务：全部挂在 runCtx 下，由退出流程在请求排空后统一取消。
	go hub.Run(runCtx)
	if cfg.Alert.Enabled {
		engine.Start(runCtx)
	}
	go offlineChecker(runCtx, nodeMgr, 10*time.Second)
	// 自监控指标周期写入时序库（走未包装的原始存储）：self_* 指标因此可查询、可画趋势，
	// 也能直接复用现有告警规则来监控 Server 自身。
	go selfmon.NewReporter(rawStore, mon, selfmon.DefaultReportInterval).Run(runCtx)
	// 数据保留：按周期清理超期数据（策略可在「系统设置 → 数据保留」调整，保存即热生效）
	go retentionMgr.Run(runCtx)
	// 报告周期化调度（全景表 11-4）：配置文件与运行状态同文件（跨重启保留上次运行时间，
	// 否则频繁重启的机器每次启动都会重新生成一份报告——报告要拉一整个周期的数据）。
	if reportSched != nil {
		go reportSched.Run(runCtx)
	}
	// 周期化巡检（同上：配置带运行状态、跨重启保留上次执行时间，避免频繁重启的机器反复体检）
	if inspectSched != nil {
		go inspectSched.Run(runCtx)
	}

	// 认证中间件（启用 auth 时保护 /api/v1/* 业务接口）。
	// authStore 显式传入（非包级单例），避免多实例部署与测试之间的状态污染。
	// 注意顺序：AuthMiddleware 必须在 AuditMiddleware 外层，
	// 由其先解析 token 写入操作者，审计中间件才能取到正确的登录用户。
	var handler http.Handler = mux
	if cfg.Auth.Enabled {
		handler = api.AuditMiddleware(mux, auditStore)
		handler = api.AuthMiddleware(handler, cfg.Auth, authStore)
	}
	// 自监控放最外层：被鉴权拒绝的请求（401/403）也要计数，
	// 否则「大量 401」这种最该被看见的信号反而看不到。
	handler = api.MetricsMiddleware(handler, mon)

	srv := newHTTPServer(cfg.Listen, handler)

	// 自行 Listen：地址不可用时立即失败，并让监听句柄可注入（便于测试）。
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		slog.Error("HTTP 监听失败", "addr", cfg.Listen, "err", err)
		cancelRun()
		_ = store.Close()
		os.Exit(1)
	}

	slog.Info("Server 启动", "mode", cfg.Mode, "listen", cfg.Listen, "tsdb", cfg.TSDB.Backend, "addr", cfg.TSDB.Addr, "version", version.Version)

	// 退出顺序：断开 WS 客户端 → 有界 Shutdown（超时则 Close）→ 取消后台任务 → 关闭存储。
	// os.Exit 会跳过 defer，故各清理动作必须在调用前显式执行。
	//
	// 首次 SIGINT/SIGTERM 后停止监听信号：恢复到系统默认处理，运维「再按一次 Ctrl+C」
	// 可立即终止，不必干等宽限期。首个信号已让 signalCtx 取消，故不影响优雅退出语义。
	stopSignalsOnExit := make(chan struct{})
	defer close(stopSignalsOnExit)
	go func() {
		select {
		case <-signalCtx.Done():
			stopSignals()
		case <-stopSignalsOnExit:
		}
	}()

	if err := serveUntilStopped(signalCtx, srv, ln, hub.CloseClients); err != nil {
		slog.Error("HTTP 服务异常退出", "err", err)
		cancelRun()
		_ = store.Close()
		os.Exit(1)
	}
	cancelRun()
	_ = store.Close()
	slog.Info("Server 已停止")
}

// receiverMux 包装 receiver 的路由注册。
type receiverMux struct {
	recv *receiver.Receiver
}

// genSecret 生成随机授权密钥（hex 编码，32 字节）。
func genSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// 退化到时间种子（极少见）
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func (m *receiverMux) register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/report", m.recv.HandleReport)
	// 集中日志上行（C2）：与上报分开，避免日志洪峰拖垮指标上报的延迟与成功率
	mux.HandleFunc("POST /api/v1/logs", m.recv.HandleLogs)
}

// offlineChecker 周期性标记离线节点。
func offlineChecker(ctx context.Context, mgr *node.Manager, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			mgr.OfflineStale()
		}
	}
}
