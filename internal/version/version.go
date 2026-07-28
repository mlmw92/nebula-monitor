// Package version 提供统一的版本信息，供 Server/Agent 编译时通过 ldflags 注入。
// 编译示例：
//
//	go build -ldflags "-X github.com/nebula/monitor/internal/version.Version=v1.0.0" ./cmd/server
package version

import "runtime"

// 以下变量通过 -ldflags "-X ...=xxx" 注入，默认值为 dev/unknown。
var (
	Version   = "dev"     // 语义化版本号，如 v1.0.0
	BuildTime = "unknown" // 构建时间，如 2026-07-26T12:00:00Z
	GoVersion = runtime.Version()
)
