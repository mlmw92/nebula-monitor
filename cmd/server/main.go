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
	"github.com/nebula/monitor/internal/server/api"
	"github.com/nebula/monitor/internal/server/config"
	"github.com/nebula/monitor/internal/server/dialtest"
	"github.com/nebula/monitor/internal/server/node"
	"github.com/nebula/monitor/internal/server/notify"
	"github.com/nebula/monitor/internal/server/receiver"
	"github.com/nebula/monitor/internal/server/report"
	"github.com/nebula/monitor/internal/server/screencfg"
	"github.com/nebula/monitor/internal/server/storage"
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

	// 告警相关
	rules := alert.NewRulesStore(cfg.Alert.RulesFile)
	alertStore := alert.NewVMAlertStore(store)
	ackStore := alert.NewAckStore(filepath.Join(filepath.Dir(cfg.Alert.RulesFile), "alert_acks.json"))
	maintenance := alert.NewMaintenanceStore(cfg.Alert.MaintenanceFile)
	notifiers := alert.BuildNotifiers(cfg.Notify)
	hub := api.NewHub()
	engine := alert.NewEngine(store, nodeMgr, rules, alertStore, notifiers, hub, maintenance, cfg.Alert.EvalInterval)

	// 通知配置管理：独立文件（Web 端可配置），启动时优先加载该文件，不存在则
	// 用 server.yaml 的 notify 段初始化并落盘；保存时通过 SetNotifiers 热加载。
	notifyMgr, err := notify.New(cfg.NotifyFile, cfg.Notify, func(c config.NotifyConfig) {
		engine.SetNotifiers(alert.BuildNotifiers(c))
	})
	if err != nil {
		slog.Error("初始化通知配置失败", "err", err)
		os.Exit(1)
	}
	// 用加载后的配置同步内存通知器（若文件存在则覆盖初始 cfg.Notify）。
	engine.SetNotifiers(alert.BuildNotifiers(notifyMgr.Get()))

	// 上报接收
	recv := receiver.New(store, nodeMgr, cfg.AgentAuth)

	// 拨测模块
	dialtestStore := dialtest.NewStore(cfg.DialtestFile)
	dialtestSched := dialtest.NewScheduler(dialtestStore, store)
	// 拨测状态跃迁联动告警事件（P2）：故障/恢复统一进入告警中心并通知。
	dialtestSched.SetSink(engine)
	dialtestSched.Start(ctx)

	// 报告生成模块
	reportGen := report.NewGenerator(store, nodeMgr, cfg.ReportDir)

	// 数据大屏模块显隐配置管理：独立文件（Web 端设置写入），不存在则用默认全开初始化并落盘。
	screenMgr, err := screencfg.New(cfg.ScreenFile, config.DefaultScreenConfig())
	if err != nil {
		slog.Error("初始化大屏配置失败", "err", err)
		os.Exit(1)
	}

	// 系统升级管理器
	var upgrader *upgrade.Manager
	if cfg.Upgrade.Enabled {
		upgrader, err = upgrade.New(upgrade.Config{
			Dir:             cfg.Upgrade.Dir,
			BinDir:          cfg.Upgrade.BinDir,
			WebDir:          cfg.WebDir,
			AgentBinDir:     cfg.AgentBinDir,
			AgentScriptPath: cfg.AgentScriptPath,
			BackupKeep:      cfg.Upgrade.BackupKeep,
			UseSystemd:      cfg.Upgrade.UseSystemd,
			Service:         cfg.Upgrade.Service,
		}, nodeMgr)
		if err != nil {
			slog.Error("初始化升级管理器失败", "err", err)
			os.Exit(1)
		}
	}

	// API
	rest := api.New(store, nodeMgr, rules, alertStore, hub, cfg.AgentAuth, cfg.WebDir, cfg.Auth, upgrader, notifyMgr, engine, maintenance, dialtestStore, reportGen, screenMgr, ackStore)
	mux := http.NewServeMux()
	recvMux := &receiverMux{recv: recv}
	recvMux.register(mux)
	rest.RegisterRoutes(mux)
	rest.RegisterDashboard(mux)
	hub.RegisterWS(mux, store)

	// Agent 分发（自带 CDN：安装脚本 + 各架构二进制）
	agentdist.New(cfg.AgentBinDir, cfg.AgentScriptPath, cfg.AgentAuth).Register(mux)

	// 启动后台任务
	go hub.Run()
	engine.Start(ctx)
	go offlineChecker(ctx, nodeMgr, 10*time.Second)

	// 认证中间件（启用 auth 时保护 /api/v1/* 业务接口）
	var handler http.Handler = mux
	if cfg.Auth.Enabled {
		handler = api.AuthMiddleware(mux, cfg.Auth)
	}

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
