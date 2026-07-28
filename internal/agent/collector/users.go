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
	for _, u := range us {
		if u.User == "" {
			continue
		}
		out = append(out, model.OnlineUser{
			User:     u.User,
			Terminal: u.Terminal,
			From:     u.Host,
			LoginAt:  time.Unix(int64(u.Started), 0).Format("2006-01-02 15:04:05"),
		})
	}
	return out
}
