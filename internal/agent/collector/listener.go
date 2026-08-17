package collector

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	gnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"

	"github.com/nebula/monitor/internal/model"
)

// collectListeners 采集当前主机的监听端口列表（TCP/UDP），类似 ss -tlnp / netstat -tlnp。
// 返回 ListenerStat 列表。
func collectListeners() []model.ListenerStat {
	conns, err := gnet.Connections("all")
	if err != nil {
		slog.Warn("采集监听端口失败", "err", err)
		return []model.ListenerStat{}
	}

	// 按 (addr, port, proto) 去重，保留每个监听地址的一条记录。
	type key struct {
		addr  string
		port  uint32
		proto string
	}
	seen := make(map[key]model.ListenerStat)

	for _, c := range conns {
		protocol, family, ok := listenerProtocol(c)
		if !ok {
			continue
		}
		// TCP 的监听状态由内核报告为 LISTEN；UDP 没有 TCP 状态，
		// 只要本地绑定了端口即可视为监听端口。
		if strings.HasPrefix(protocol, "tcp") && c.Status != "LISTEN" {
			continue
		}
		if strings.HasPrefix(protocol, "udp") && c.Laddr.Port == 0 {
			continue
		}

		addr := c.Laddr.IP
		if family == "ipv4" && (addr == "" || addr == "::") {
			addr = "0.0.0.0"
		}
		if family == "ipv6" && addr == "" {
			addr = "::"
		}
		k := key{addr: addr, port: c.Laddr.Port, proto: protocol}
		if _, exists := seen[k]; exists {
			continue
		}

		var pid int32
		var procName, exePath string
		if c.Pid > 0 {
			pid = c.Pid
			p, err := process.NewProcess(c.Pid)
			if err == nil {
				procName, _ = p.Name()
				exePath, _ = p.Exe()
			}
		}

		state := c.Status
		if state == "" && strings.HasPrefix(protocol, "udp") {
			state = "UNCONN"
		}
		seen[k] = model.ListenerStat{
			Addr:     addr,
			Port:     c.Laddr.Port,
			Protocol: protocol,
			State:    state,
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
	sort.Slice(result, func(i, j int) bool {
		if result[i].Port != result[j].Port {
			return result[i].Port < result[j].Port
		}
		if result[i].Protocol != result[j].Protocol {
			return result[i].Protocol < result[j].Protocol
		}
		return result[i].Addr < result[j].Addr
	})
	return result
}

// listenerProtocol derives the protocol/family without relying on string conversion
// of gopsutil's numeric Family field. Address parsing keeps this helper portable
// across the operating systems supported by gopsutil.
func listenerProtocol(c gnet.ConnectionStat) (protocol, family string, ok bool) {
	ip := net.ParseIP(c.Laddr.IP)
	if ip == nil {
		return "", "", false
	}
	if ip.To4() != nil {
		family = "ipv4"
	} else {
		family = "ipv6"
	}

	switch c.Type {
	case 1: // syscall.SOCK_STREAM
		protocol = "tcp"
	case 2: // syscall.SOCK_DGRAM
		protocol = "udp"
	default:
		return "", "", false
	}
	if family == "ipv6" {
		protocol += "6"
	}
	return protocol, family, true
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
