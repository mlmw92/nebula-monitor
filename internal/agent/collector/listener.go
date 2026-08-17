package collector

import (
	"fmt"
	gnet "github.com/shirou/gopsutil/v4/net"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/shirou/gopsutil/v4/process"

	"github.com/nebula/monitor/internal/model"
)

// collectListeners 采集当前主机的监听端口列表（TCP/UDP），类似 ss -tlnp / netstat -tlnp。
// 返回 ListenerStat 列表。
func collectListeners() []model.ListenerStat {
	conns, err := gnet.Connections("all")
	if err != nil {
		return nil
	}

	// 按 (addr, port, proto) 去重，保留每个监听地址的一条记录
	type key struct {
		addr string
		port uint32
		proto string
	}
	seen := make(map[key]model.ListenerStat)

	for _, c := range conns {
		if c.Status != "LISTEN" {
			continue
		}
		addr := c.Laddr.IP
		port := c.Laddr.Port
		proto := strings.ToLower(string(c.Family)) // "inet" / "inet6"
		switch proto {
		case "inet":
			proto = "tcp"
			if addr == "::" || addr == "" {
				addr = "0.0.0.0"
			}
		case "inet6":
			proto = "tcp6"
			if addr == "" || addr == "::" {
				addr = "::"
			}
		default:
			continue
		}
		k := key{addr: addr, port: port, proto: proto}
		if _, exists := seen[k]; exists {
			continue
		}

		var pid int32
		var procName, exePath string
		if c.Pid > 0 {
			pid = int32(c.Pid)
			p, err := process.NewProcess(c.Pid)
			if err == nil {
				name, _ := p.Name()
				procName = name
				exePath, _ = p.Exe()
			}
		}

		family := "ipv4"
		if strings.HasSuffix(proto, "6") {
			family = "ipv6"
		}

		seen[k] = model.ListenerStat{
			Addr:     addr,
			Port:     port,
			Protocol: proto,
			State:    "LISTEN",
			PID:      pid,
			Process:  procName,
			ExePath:  exePath,
			Family:   family,
		}
	}

	result := make([]model.ListenerStat, 0, len(seen))
	for _, v := range seen {
		result = append(result, v)
	}
	return result
}

// resolveProcExeByPID 通过 /proc/<pid>/exe 获取进程可执行路径（备用方案）。
func resolveProcExeByPID(pid int32) string {
	link := filepath.Join("/proc", strconv.Itoa(int(pid)), "exe")
	target, err := os.Readlink(link)
	if err != nil {
		return ""
	}
	// 如果路径被截断（如 (deleted) 后缀），清理它
	target = strings.TrimSuffix(target, " (deleted)")
	return target
}

// formatAddrPort 将 IP + port 格式化为可读的监听地址字符串。
func formatAddrPort(ip string, port uint32) string {
	if ip == "0.0.0.0" || ip == "::" || ip == "" {
		return fmt.Sprintf(":%d", port)
	}
	return net.JoinHostPort(ip, strconv.Itoa(int(port)))
}
