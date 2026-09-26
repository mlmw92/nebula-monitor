package collector

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// holdConnections 启动一个「假 Redis 服务端」：接受连接后保持打开但永不写入响应，
// 用于模拟服务端卡死（TCP 可达、RESP 无响应）的场景。
func holdConnections(t *testing.T, ln net.Listener) {
	t.Helper()
	go func() {
		var held []net.Conn
		defer func() {
			for _, c := range held {
				_ = c.Close()
			}
		}()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			held = append(held, conn)
		}
	}()
}

// TestRedisInfo_CtxTimeoutCancelsInFlightRead 验证「一次到位」的核心收益：
// 服务端接受连接但永不响应时，ctx 超时必须立即中止在途 RESP 读，而不是阻塞到 TCP 层超时。
//
// 若 sendCommand 未通过 context.AfterFunc + conn.SetDeadline 取消在途读，
// 本用例会因等待底层读超时而显著超过 1s。
func TestRedisInfo_CtxTimeoutCancelsInFlightRead(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动假 Redis 监听失败: %v", err)
	}
	defer ln.Close()
	holdConnections(t, ln)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = redisInfo(ctx, ln.Addr().String(), "", "all")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("期望 ctx 超时导致采集失败，实际返回成功")
	}
	if elapsed > time.Second {
		t.Fatalf("ctx 超时未能取消在途 RESP 读（疑似缺少 SetDeadline），耗时 %v", elapsed)
	}
}

// TestRedisInfo_CtxAlreadyCanceled 验证已取消的 ctx 不会发起连接，立即失败。
func TestRedisInfo_CtxAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	if _, err := redisInfo(ctx, "127.0.0.1:1", "", "all"); err == nil {
		t.Fatal("期望已取消的 ctx 直接失败，实际返回成功")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("已取消的 ctx 应立即返回，实际耗时 %v", elapsed)
	}
}

// TestRedisCollectCtx_StopsAfterCancel 验证实例循环内的 ctx 检查：
// 第一个实例耗尽超时后，后续实例不再被尝试，且整体在超时后及时返回。
func TestRedisCollectCtx_StopsAfterCancel(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动假 Redis 监听失败: %v", err)
	}
	defer ln.Close()
	holdConnections(t, ln)

	addr := ln.Addr().String()
	c := NewRedisCollector("test-node", []model.RedisInstanceConfig{
		{Name: "stuck-1", Addr: addr, Topology: "standalone"},
		{Name: "stuck-2", Addr: addr, Topology: "standalone"},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	var instances []model.RedisInstance
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, instances = c.CollectCtx(ctx)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("CollectCtx 未在 ctx 超时后及时返回")
	}

	if len(instances) != 1 {
		t.Fatalf("期望 ctx 超时后仅采集第一个实例，实际实例数 %d", len(instances))
	}
	if instances[0].Up {
		t.Fatal("卡死的实例不应被标记为在线")
	}
}
