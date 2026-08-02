// config_adapter.go 提供 config.ProxyConfig 到 edgeCfg/hubCfg 的适配函数。
package proxy

import "github.com/nebula/monitor/internal/agent/config"

// EdgeCfgFromConfig 从 Agent 配置的 ProxyConfig 构建 Edge 运行配置。
func EdgeCfgFromConfig(p config.ProxyConfig) edgeCfg {
	return edgeCfg{
		Listen:     p.Listen,
		HubAddr:    p.HubAddr,
		TLSCert:    p.TLSCert,
		TLSKey:     p.TLSKey,
		TLSCA:      p.TLSCA,
		BufferSize: p.BufferSize,
		PoolSize:   p.PoolSize,
	}
}

// HubCfgFromConfig 从 Agent 配置的 ProxyConfig 构建 Hub 运行配置。
func HubCfgFromConfig(p config.ProxyConfig) hubCfg {
	return hubCfg{
		Listen:  p.Listen,
		TLSCert: p.TLSCert,
		TLSKey:  p.TLSKey,
		TLSCA:   p.TLSCA,
	}
}
