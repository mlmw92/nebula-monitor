package collector

import (
	"path/filepath"
	"strings"

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
