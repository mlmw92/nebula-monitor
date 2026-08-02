// reconnector.go 实现断线重连：指数退避 + 随机抖动 + 重连后缓冲补发。
package proxy

import (
	"log/slog"
	"math/rand"
	"time"
)

// Reconnector 管理重连节奏。
type Reconnector struct {
	base      time.Duration
	cap       time.Duration
	metrics   *Metrics
	attempt   int
}

// NewReconnector 创建重连器。base 为初始退避，cap 为上限。
func NewReconnector(base, cap time.Duration, metrics *Metrics) *Reconnector {
	if base <= 0 {
		base = 2 * time.Second
	}
	if cap <= 0 {
		cap = 60 * time.Second
	}
	return &Reconnector{base: base, cap: cap, metrics: metrics}
}

// NextBackoff 返回下一次重连前的等待时长，并递增尝试次数。
// 指数退避 + ±20% 随机抖动，避免重连风暴。
func (r *Reconnector) NextBackoff() time.Duration {
	r.attempt++
	d := r.base << uint(r.attempt-1)
	if d > r.cap {
		d = r.cap
	}
	// 抖动 ±20%
	jitter := time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
	if r.metrics != nil {
		r.metrics.ReconnectTotal.Add(1)
	}
	slog.Info("准备重连", "attempt", r.attempt, "backoff", jitter)
	return jitter
}

// Reset 重置尝试次数（连接成功后调用）。
func (r *Reconnector) Reset() {
	r.attempt = 0
}

// Attempt 返回当前尝试次数。
func (r *Reconnector) Attempt() int {
	return r.attempt
}
