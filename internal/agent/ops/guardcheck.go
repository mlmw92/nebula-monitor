package ops

import (
	"os/exec"
	"sort"
	"strings"

	"github.com/nebula/monitor/internal/agent/config"
)

// 本文件处理一件事：**把「两个护栏配在一起就等于任意代码执行」这个隐性依赖显式化**。
//
// 为什么需要它：file.push 的护栏是「允许写的目录前缀」，svc.restart 的护栏是「允许重启的单元清单」，
// 各自看都正常、各自都有测试。但若同一台机器同时允许「写某个可重启服务的可执行文件所在目录」
// 与「重启该服务」，那么"推一个文件 + 重启"就等于任意代码执行 —— **不需要任何"任意命令"能力**。
// 配置的人不会意识到自己配出了一个 RCE，界面上也没有任何提示（这条组合此前没有任何检查）。
//
// 还有一类更短的路：系统**自己会执行**的目录（systemd 单元目录、cron 目录、profile.d、
// ld.so.conf.d、sudoers.d、root 的 authorized_keys）。往这些地方写文件**连重启都不需要**，
// 等下一次登录 / 下一次 cron / 下一次动态链接即可生效，所以它们与 units 清单无关，单独检查。

// GuardRisk 是一条「护栏组合导致任意代码执行」的风险。
type GuardRisk struct {
	// Kind 命中类型：exec（可重启服务的可执行文件落在允许写入的目录里）
	// / persistence（允许写入的是系统会自己执行的目录）。
	Kind string
	// AllowedDir 是 file.push 允许写入的目录（配置里的形态）。
	AllowedDir string
	// Exposed 是因此暴露出来的东西：kind=exec 时是单元的可执行文件路径，
	// kind=persistence 时是系统会自动执行的那个目录。
	Exposed string
	// Unit 只在 kind=exec 时有值：那个可重启的单元。
	Unit string
}

// Describe 生成给人看的一句话（日志与后续的界面展示共用，避免两处措辞不一致）。
func (r GuardRisk) Describe() string {
	if r.Kind == "exec" {
		return "护栏组合风险：允许写入的目录「" + r.AllowedDir + "」覆盖了可重启单元「" + r.Unit +
			"」的可执行文件「" + r.Exposed + "」——推一个文件再重启该服务，等于任意代码执行"
	}
	return "护栏组合风险：允许写入的目录「" + r.AllowedDir + "」覆盖了系统会自动执行的目录「" + r.Exposed +
		"」——写入即持久化，不需要重启任何服务"
}

// persistenceDirs 是「系统自己会执行其内容」的目录。
//
// 只列目录不列单个文件：护栏本身就是**目录前缀**语义，用单个文件（如 /etc/ld.so.preload）
// 去比对前缀会得到毫无意义的结论。因此这里选的都是"往里放文件就会被执行"的目录。
var persistenceDirs = []string{
	"/etc/systemd/system", "/usr/lib/systemd/system", "/lib/systemd/system",
	"/etc/init.d", "/etc/cron.d", "/etc/cron.hourly", "/etc/cron.daily",
	"/etc/cron.weekly", "/etc/cron.monthly", "/var/spool/cron",
	"/etc/profile.d", "/etc/ld.so.conf.d", "/etc/sudoers.d",
	"/root/.ssh", // authorized_keys：加一把钥匙等于给自己开一个免密入口
}

// CheckGuardRisks 检查本机护栏的组合是否等价于任意代码执行，返回风险清单（空 = 没命中）。
//
// 刻意**只告警、不拒绝**：这两项都是用户明确写下的同意（他同意写那个目录、也同意重启那个单元），
// 系统替他悄悄禁掉其中一个，比让风险可见更难排查，也违背「机器自身的同意优先于中心授权」。
// 把组合说出来，由他决定收掉哪一个 —— 本函数因此只做判定，不做任何拦截。
//
// executableOf 由调用方注入（真实实现是 SystemdExecutables），便于测试；
// 返回空表示"这个单元的可执行文件在哪我们不知道"，此时只跳过 exec 类检查——
// **不命中不等于安全**，所以 persistence 类的检查与它无关地照常执行。
func CheckGuardRisks(g config.OpsGuards, executableOf func(unit string) []string) []GuardRisk {
	if !g.File.OpsFileEnabled() {
		return nil
	}
	dirs := g.File.OpsAllowedDirs()
	forms := allowedDirForms(dirs)
	var out []GuardRisk

	// ① 系统自己会执行的目录：与"能不能重启服务"无关
	for _, dir := range dirs {
		for _, exposed := range persistenceDirs {
			if inAllowedDirOrSelf(exposed, []string{dir}) {
				out = append(out, GuardRisk{Kind: "persistence", AllowedDir: dir, Exposed: exposed})
			}
		}
	}

	// ② 可重启服务的可执行文件，落在允许写入的目录里
	if g.Write {
		// 单元集合是 map，遍历顺序随机；先排序再处理，让日志与用例的输出稳定可比
		// （每次启动打印顺序都不一样的话，diff 日志时会以为是配置变了）。
		units := make([]string, 0, len(g.Units))
		for unit := range g.OpsAllowedUnits() {
			units = append(units, unit)
		}
		sort.Strings(units)
		for _, unit := range units {
			for _, exe := range executableOf(unit) {
				if dir, ok := coveringDir(exe, forms); ok {
					out = append(out, GuardRisk{Kind: "exec", AllowedDir: dir, Exposed: exe, Unit: unit})
				}
			}
		}
	}
	return out
}

// coveringDir 返回覆盖该路径的允许目录（取第一个命中的形态，含软链解析后的形态）。
func coveringDir(target string, dirs []string) (string, bool) {
	for _, d := range dirs {
		if strings.HasPrefix(target, d+"/") {
			return d, true
		}
	}
	return "", false
}

// SystemdExecutables 返回某 systemd 单元实际执行的可执行文件/脚本路径（取 ExecStart 与 ExecReload）。
//
// 读不到就返回 nil（非 systemd 机器、单元不存在、systemctl 不可用）：
// 这时"可执行文件在哪"无从得知，于是 ② 类检查不命中——**宁可少报，也不要按单元名猜路径**，
// 猜错的结果是没人会去核这条告警。
func SystemdExecutables(unit string) []string {
	out, err := exec.Command("systemctl", "show", "-p", "ExecStart", "-p", "ExecReload", unit).Output()
	if err != nil {
		return nil
	}
	return parseExecPaths(string(out))
}

// parseExecPaths 从 systemctl show 的输出里取 path= 的值。
//
// 形态：ExecStart={ path=/usr/sbin/nginx ; argv[]=/usr/sbin/nginx -g "daemon off;" ; ignore_errors=no ; ... }
// 一个单元可能有多个 ExecStart/ExecReload 行，逐个取。
func parseExecPaths(out string) []string {
	var res []string
	for _, line := range strings.Split(out, "\n") {
		i := strings.Index(line, "path=")
		if i < 0 {
			continue
		}
		rest := line[i+len("path="):]
		if j := strings.Index(rest, " ;"); j >= 0 {
			rest = rest[:j]
		}
		if rest = strings.TrimSpace(rest); rest != "" {
			res = append(res, rest)
		}
	}
	return res
}
