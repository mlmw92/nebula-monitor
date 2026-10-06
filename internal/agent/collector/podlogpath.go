package collector

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// parsePodLogPath 从 kubelet 的容器日志路径解析容器身份：
//
//	/var/log/pods/<namespace>_<pod>_<uid>/<container>/<重启序号>.log
//
// 解析不出来返回 nil，调用方据此决定「照常采集但不带身份」。
//
// 为什么在采集侧解析、只上报身份而不是上报路径：路径会暴露被监控机的目录结构
// （而检索与资产联动只需要"属于哪个 Pod"）；解析规则放在**读文件的那一侧**，
// 上报的就只有三个名字。
//
// 这里**只按「末三段的布局」解析，不校验前缀**：前缀（kubelet 的容器日志目录）由
// 配置层保证——podLogs 来源的 paths 在启动期就被要求落在 config.PodLogDir 下。
// 两处各管一件事的好处是解析逻辑可以脱离真实目录单独测（本机开发环境没有 kubelet）。
//
// 各段的合法性交给 model.NormalizeLogOrigin 统一判定（服务端用同一条规则校验）。
func parsePodLogPath(path string) *model.LogOrigin {
	// 取末三段：<ns>_<pod>_<uid>/<container>/<n>.log
	parts := strings.Split(filepath.ToSlash(path), "/")
	if len(parts) < 3 {
		return nil
	}
	parts = parts[len(parts)-3:]
	// k8s 的 namespace / pod 名都是 DNS 标签（不含下划线），因此按 `_` 切分不会有歧义；
	// UID 是带连字符的 UUID，同样不含下划线。
	head := strings.Split(parts[0], "_")
	if len(head) != 3 {
		return nil
	}
	// 文件名是"重启序号"（0.log、1.log…）。它不参与身份，但格式不对说明这不是
	// kubelet 写的容器日志文件（可能是同目录下的别的东西），此时宁可不给身份。
	if !isPodLogFileName(parts[2]) {
		return nil
	}
	origin, ok := model.NormalizeLogOrigin(&model.LogOrigin{
		Namespace: head[0],
		Pod:       head[1],
		Container: parts[1],
	})
	if !ok {
		return nil
	}
	return origin
}

// parseCRILogLine 解析 kubelet 的 CRI 日志行：
//
//	<RFC3339Nano> <stdout|stderr> <F|P> <内容>
//
// 返回 (毫秒时间戳, 内容, 是否匹配该格式)。
//
// 为什么必须解析它：容器日志行的时间与内容都在这层**框架**里。不剥掉它，
// 正文里每一行都带着"时间 + 流 + 标记"噪声；不取它的时间，就只能用采集时刻，
// 于是同一轮采集的所有行挤在同一个毫秒上——时间范围过滤与排序都会退化
// （2026-10-06 端到端实机验证时正是这样：三行 ts 完全相同）。
//
// 只对 podLogs 来源调用：CRI 框架只出现在 kubelet 写的容器日志里，
// 普通文件里"恰好长这样"的一行不该被改写。
func parseCRILogLine(line string) (int64, string, bool) {
	// 时间戳在第一个空格之前
	i := strings.IndexByte(line, ' ')
	if i <= 0 {
		return 0, "", false
	}
	ts, err := time.Parse(time.RFC3339Nano, line[:i])
	if err != nil {
		return 0, "", false
	}
	rest := line[i+1:]
	// <流> <标记> <内容>
	j := strings.IndexByte(rest, ' ')
	if j <= 0 {
		return 0, "", false
	}
	switch rest[:j] {
	case "stdout", "stderr":
	default:
		return 0, "", false
	}
	rest = rest[j+1:]
	// 标记：F=完整一条，P=部分（多行写入被运行时按行拆分，最后一行才是 F）。
	// 这里不参与合并（那是 multiline 配置的职责），但格式不对就不认这条框架。
	if len(rest) < 2 || rest[1] != ' ' {
		return 0, "", false
	}
	switch rest[0] {
	case 'F', 'P':
	default:
		return 0, "", false
	}
	return ts.UnixMilli(), rest[2:], true
}

// isPodLogFileName 判断文件名是否为 kubelet 的容器日志名（`<数字>.log`）。
func isPodLogFileName(name string) bool {
	if !strings.HasSuffix(name, ".log") {
		return false
	}
	digits := strings.TrimSuffix(name, ".log")
	if digits == "" {
		return false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
