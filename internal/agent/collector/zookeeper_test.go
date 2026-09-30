package collector

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// fakeZooKeeper 启动一个假 ZooKeeper：接受连接后回写 ZooKeeper 3.x `mntr` 的**真实输出格式**
// ——制表符分隔的 `zk_key\tvalue` 行（这是回归重点：早期实现按 `key=value` 解析，
// 导致所有实例恒判离线）。
func fakeZooKeeper(t *testing.T, payload string) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动假 ZooKeeper 监听失败: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 16)
				_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
				_, _ = c.Read(buf) // 读取 mntr 命令
				_, _ = c.Write([]byte(payload))
			}(conn)
		}
	}()
	return ln
}

// mntrSample 是 ZooKeeper 3.9 实测输出（制表符分隔，取自真实 standalone 实例）。
const mntrSample = "zk_version\t3.9.5-293c895a8d966a3ecb92872be4a1daf87d725da2, built on 2026-02-11 20:18 UTC\n" +
	"zk_avg_latency\t0\n" +
	"zk_max_latency\t0\n" +
	"zk_min_latency\t0\n" +
	"zk_num_alive_connections\t1\n" +
	"zk_outstanding_requests\t0\n" +
	"zk_server_state\tstandalone\n" +
	"zk_znode_count\t4\n"

// TestZooKeeperCollect_TabSeparatedMntr mntr 用制表符分隔时必须能解析出指标且判在线。
func TestZooKeeperCollect_TabSeparatedMntr(t *testing.T) {
	ln := fakeZooKeeper(t, mntrSample)
	defer ln.Close()

	c := NewZooKeeperCollector("test-node", []model.ZooKeeperInstanceConfig{
		{Name: "zk-01", Addr: ln.Addr().String()},
	})
	metrics, instances := c.CollectCtx(context.Background())

	if len(instances) != 1 || !instances[0].Up {
		t.Fatalf("实例应判为在线，实际 %+v", instances)
	}
	if instances[0].Role != "standalone" {
		t.Errorf("角色应取 zk_server_state，got %q", instances[0].Role)
	}
	if instances[0].Version == "" {
		t.Errorf("版本应取 zk_version，got 空")
	}
	if instances[0].ZnodeCount != 4 || instances[0].AliveConnections != 1 {
		t.Errorf("指标解析不符：znode=%v conns=%v", instances[0].ZnodeCount, instances[0].AliveConnections)
	}

	byName := map[string]float64{}
	for _, m := range metrics {
		byName[m.Name] = m.Value
	}
	if byName["zookeeper_instance_up"] != 1 {
		t.Errorf("zookeeper_instance_up 应为 1，got %v", byName["zookeeper_instance_up"])
	}
	if byName["zookeeper_znode_count"] != 4 {
		t.Errorf("zookeeper_znode_count 应为 4，got %v", byName["zookeeper_znode_count"])
	}
}

// TestZooKeeperCollect_EqualsSeparatorStillWorks 兼容 "key=value" 形态（测试替身/代理改写）。
func TestZooKeeperCollect_EqualsSeparatorStillWorks(t *testing.T) {
	ln := fakeZooKeeper(t, "zk_server_state=leader\nzk_znode_count=7\n")
	defer ln.Close()

	c := NewZooKeeperCollector("test-node", []model.ZooKeeperInstanceConfig{
		{Name: "zk-02", Addr: ln.Addr().String()},
	})
	_, instances := c.CollectCtx(context.Background())
	if len(instances) != 1 || !instances[0].Up || instances[0].ZnodeCount != 7 {
		t.Fatalf("等号分隔输出应同样可解析，实际 %+v", instances)
	}
}

// TestZooKeeperCollect_EmptyMntrMarksDown 四字命令被禁用（无输出）时判离线。
func TestZooKeeperCollect_EmptyMntrMarksDown(t *testing.T) {
	ln := fakeZooKeeper(t, "")
	defer ln.Close()

	c := NewZooKeeperCollector("test-node", []model.ZooKeeperInstanceConfig{
		{Name: "zk-03", Addr: ln.Addr().String()},
	})
	metrics, instances := c.CollectCtx(context.Background())
	if len(instances) != 1 || instances[0].Up {
		t.Fatalf("无输出应判离线，实际 %+v", instances)
	}
	if len(metrics) != 1 || metrics[0].Name != "zookeeper_instance_up" || metrics[0].Value != 0 {
		t.Fatalf("离线时只应产出 up=0，实际 %+v", metrics)
	}
}
