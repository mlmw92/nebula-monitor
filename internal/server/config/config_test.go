package config

import (
	"os"
	"path/filepath"
	"testing"
)

// 安装脚本生成的集中日志配置必须被服务端**读懂**。
//
// 为什么需要这条契约用例：install-server.sh 写的是 YAML 键名（logDir / logBackend /
// logVictoriaLogs.addr），而服务端认的是结构体 tag。两边任一处改名，症状都是
// **静默降级**——运维按安装脚本切到了外部日志后端，服务端却仍在用自研落盘
// （或反过来），没有任何报错，直到有人发现"日志怎么还在本地"。
//
// 下面的 YAML 片段**逐字来自 deploy/install-server.sh 生成的 log 块**
// （见该脚本 write_config 里的 $log_block 与周边注释）。改脚本时必须同步改这里，
// 反之亦然——两处不一致本身就是这条用例要暴露的问题。
func writeConfigFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "server.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	return path
}

func TestLoadInstallerGeneratedLogConfig(t *testing.T) {
	t.Run("外部后端 victorialogs", func(t *testing.T) {
		path := writeConfigFile(t, `mode: standalone
listen: ":8080"
tsdb:
  backend: victoriametrics
  addr: "http://127.0.0.1:8428"

# 集中日志存储
#   logBackend: local（默认，自研按 来源/日期/节点 分片落盘）
#              | victorialogs（外部后端，由 deploy/install-logs.sh 安装）
#   切到外部后端后：日志的保留与容量由该后端负责（它的 -retentionPeriod），
#   平台不再执行日志清理、也不再叠加「单来源每日上限」；logDir 不再被使用。
#   上行的限速（logUploadRateBps）与请求体上限（logMaxBodyBytes）仍然生效——它们在接收侧。
logDir: "/var/lib/monitor-server/logs"
logBackend: victorialogs
logVictoriaLogs:
  addr: "http://10.0.0.10:9428"
`)
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("加载配置失败: %v", err)
		}
		if cfg.LogBackend != "victorialogs" {
			t.Fatalf("logBackend 未生效: %q（安装脚本与结构体 tag 可能已不一致）", cfg.LogBackend)
		}
		if cfg.LogVictoriaLogs.Addr != "http://10.0.0.10:9428" {
			t.Fatalf("logVictoriaLogs.addr 未生效: %q", cfg.LogVictoriaLogs.Addr)
		}
		if cfg.LogDir != "/var/lib/monitor-server/logs" {
			t.Fatalf("logDir 未生效: %q", cfg.LogDir)
		}
	})

	t.Run("默认后端 local", func(t *testing.T) {
		path := writeConfigFile(t, `mode: standalone
logDir: "/var/lib/monitor-server/logs"
logBackend: local
`)
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("加载配置失败: %v", err)
		}
		if cfg.LogBackend != "local" {
			t.Fatalf("logBackend 应为 local: %q", cfg.LogBackend)
		}
		// 本地后端下不写 logVictoriaLogs 段：零值即"未配置"，不得影响默认值
		if cfg.LogVictoriaLogs.Addr != "" {
			t.Fatalf("未配置时 addr 应为空: %q", cfg.LogVictoriaLogs.Addr)
		}
	})

	// 兼容性：这次改动之前生成的 server.yaml 里**没有** logBackend 键，
	// 它们必须继续按默认（local）运行——升级不该把已有部署的日志弄丢。
	t.Run("旧配置缺 logBackend 时回落默认 local", func(t *testing.T) {
		path := writeConfigFile(t, `mode: standalone
listen: ":8080"
tsdb:
  backend: victoriametrics
  addr: "http://127.0.0.1:8428"
`)
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("加载配置失败: %v", err)
		}
		if cfg.LogBackend != "local" {
			t.Fatalf("缺省应为 local（向后兼容）: %q", cfg.LogBackend)
		}
		if cfg.LogDir != "/var/lib/monitor-server/logs" {
			t.Fatalf("logDir 应取默认值: %q", cfg.LogDir)
		}
	})
}
