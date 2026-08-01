package collector

import (
	"time"

	"github.com/shirou/gopsutil/v4/host"

	"github.com/nebula/monitor/internal/model"
)

// CollectUsers 采集当前在线 SSH 用户会话（含终端、来源与登录时间）。
// 用于主机详情「在线 SSH 用户」展示。读取系统 utmp，兼容 Linux。
func CollectUsers() []model.OnlineUser {
	us, err := host.Users()
	if err != nil {
		return nil
	}
	out := make([]model.OnlineUser, 0, len(us))
	// utmp 可能针对同一终端(tty/pts)写入多条 USER_PROCESS 记录
	// （如 SSH 连接 + PAM 会话 + 未清理的残留记录），导致同一登录会话被重复计数。
	// 按 终端 去重：一个终端同时只能有一个会话，保留该终端上登录时间最新的一条。
	termIdx := make(map[string]int)     // terminal -> 在 out 中的下标
	termStart := make(map[string]int) // terminal -> 对应记录的 Start 时间
	for _, u := range us {
		if u.User == "" || u.Terminal == "" {
			continue
		}
		ou := model.OnlineUser{
			User:     u.User,
			Terminal: u.Terminal,
			From:     u.Host,
			LoginAt:  time.Unix(int64(u.Started), 0).Format("2006-01-02 15:04:05"),
		}
		if idx, ok := termIdx[u.Terminal]; ok {
			if u.Started > termStart[u.Terminal] {
				termStart[u.Terminal] = u.Started
				out[idx] = ou
			}
			continue
		}
		termIdx[u.Terminal] = len(out)
		termStart[u.Terminal] = u.Started
		out = append(out, ou)
	}
	return out
}
