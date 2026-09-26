// Command server 是中心服务端：接收上报、写 VM、提供 API/仪表盘与告警。
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/nebula/monitor/internal/server/agentdist"
	"github.com/nebula/monitor/internal/server/alert"
	"github.com/nebula/monitor/internal/server/analysis"
	"github.com/nebula/monitor/internal/server/api"
	"github.com/nebula/monitor/internal/server/audit"
	"github.com/nebula/monitor/internal/server/auth"
	"github.com/nebula/monitor/internal/server/config"
	servercrypto "github.com/nebula/monitor/internal/server/crypto"
	"github.com/nebula/monitor/internal/server/dashboard"
	"github.com/nebula/monitor/internal/server/dialtest"
	"github.com/nebula/monitor/internal/server/instancereg"
	"github.com/nebula/monitor/internal/server/logstore"
	"github.com/nebula/monitor/internal/server/mwreg"
	"github.com/nebula/monitor/internal/server/nginxaccess"
	"github.com/nebula/monitor/internal/server/node"
	"github.com/nebula/monitor/internal/server/notify"
	"github.com/nebula/monitor/internal/server/receiver"
	"github.com/nebula/monitor/internal/server/report"
	"github.com/nebula/monitor/internal/server/retention"
	"github.com/nebula/monitor/internal/server/screencfg"
	"github.com/nebula/monitor/internal/server/security"
	"github.com/nebula/monitor/internal/server/selfmon"
	"github.com/nebula/monitor/internal/server/storage"
	"github.com/nebula/monitor/internal/server/templates"
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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

	// 上报接收（Nginx access log 地理聚合窗口：TTL 1h，实时大屏场景）
	ngxWin := nginxaccess.NewWindow(nginxaccess.NewGeo(), time.Hour)
	// 安全事件/基线存储（JSON 持久化，默认安全能力启用）
	securityStore := security.New(cfg.SecurityStoreFile)
	defenseStore := security.NewDefenseStore(filepath.Join(filepath.Dir(cfg.SecurityStoreFile), "defense_tasks.json"))
	recv := receiver.New(store, nodeMgr, cfg.AgentAuth, ngxWin, securityStore, engine, defenseStore)

	// 采集项模板（C1 阶段二）：Web 端统一 CRUD，并作为下发给 Agent 的数据源
	templateStore := templates.NewStore(cfg.TemplatesFile)
	recv.SetTemplateStore(templateStore)
	// 集中日志（C2）：目录留空时取 <DataDir>/logs；未配置时该能力关闭（接口回 503）。
	// 三个上限都必须有值——「开了日志把盘写满」不是会不会的问题，只是时间问题。
	logDir := cfg.LogDir
	if logDir == "" {
		logDir = filepath.Join(cfg.DataDir, "logs")
	}
	recv.SetLogStore(logstore.New(logDir, cfg.LogMaxBytesPerDay), cfg.LogMaxBodyBytes, cfg.LogUploadRateBps)
	// 中间件类型注册表：内置 10 类 + 由模板派生的类型，是「有哪些中间件类型」的唯一来源
	//（api 的类型清单、报告分节、告警的服务类型校验都读它）。
	mwRegistry := mwreg.New(templateStore)

	// 拨测模块
	dialtestStore := dialtest.NewStore(cfg.DialtestFile)
	dialtestSched := dialtest.NewScheduler(dialtestStore, store)
	// 拨测状态跃迁联动告警事件（P2）：故障/恢复统一进入告警中心并通知。
	if cfg.Alert.Enabled {
		dialtestSched.SetSink(engine)
	}
	dialtestSched.Start(ctx)

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
	rest := api.New(store, nodeMgr, rules, alertStore, hub, cfg.AgentAuth, cfg.AgentBinDir, cfg.WebDir, cfg.Auth, upgrader, notifyMgr, engine, maintenance, dialtestStore, reportGen, screenMgr, ackStore, inhibitStore, groupingStore, ngxWin, uiMgr, *cfgPath, securityStore, defenseStore, auditStore, authStore)
	rest.SetDashboardManager(dashMgr)
	rest.SetAnalyzer(analyzer)
	rest.SetPipelineStore(pipelineStore)
	rest.SetSelfMon(mon)
	rest.SetRetention(retentionMgr)
	rest.SetTemplateStore(templateStore)
	rest.SetMiddlewareRegistry(mwRegistry)
	// 告警侧同样读注册表：模板派生类型才能被「服务离线」规则监控
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

	// 启动后台任务
	go hub.Run()
	if cfg.Alert.Enabled {
		engine.Start(ctx)
	}
	go offlineChecker(ctx, nodeMgr, 10*time.Second)
	// 自监控指标周期写入时序库（走未包装的原始存储）：self_* 指标因此可查询、可画趋势，
	// 也能直接复用现有告警规则来监控 Server 自身。
	go selfmon.NewReporter(rawStore, mon, selfmon.DefaultReportInterval).Run(ctx)
	// 数据保留：按周期清理超期数据（策略可在「系统设置 → 数据保留」调整，保存即热生效）
	go retentionMgr.Run(ctx)

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

	srv := &http.Server{
		Addr:    cfg.Listen,
		Handler: handler,
	}

	slog.Info("Server 启动", "mode", cfg.Mode, "listen", cfg.Listen, "tsdb", cfg.TSDB.Backend, "addr", cfg.TSDB.Addr, "version", version.Version)
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("HTTP 服务异常", "err", err)
		os.Exit(1)
	}
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
