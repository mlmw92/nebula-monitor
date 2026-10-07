package collector

import "testing"

// PG 主库地址解析（批次 21 的 D4：`PostgresInstance.ReplicaOf` 此前没有任何采集器填它，
// 于是"副本 → 主库"这条依赖对 postgres 永远建不起来——数据其实就在 pg_stat_wal_receiver.conninfo 里）。
//
// 单独测这段是因为真实路径要先有一个 standby，而 dev-server 的 PG 是单机，端到端跑不到这里；
// 而这恰恰是细节最容易错的地方：值可能带引号、host 与 port 必须**同时**存在。
//
// 回环的情形刻意不断言具体取值：normalizeRemoteAddr 会把 `127.0.0.1` 换成 Agent 的物理网卡 IP
// （这正是实机上台账键写作 `10.0.0.10:700x` 而不是 `127.0.0.1:700x` 的原因，也是"副本的
// replicaOf 与主库资产的实例键能对上"的前提），而测试机上取到哪个 IP 不稳定。
func TestParsePGConninfo(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"常见形态", "host=10.0.0.9 port=5432 user=repl application_name=walreceiver", "10.0.0.9:5432"},
		{"值带引号（libpq 在值含空格时会加）", "host='10.0.0.9' port='5432' user=repl", "10.0.0.9:5432"},
		{"字段顺序无关", "user=repl port=5433 host=db1.internal", "db1.internal:5433"},
		{"域名原样保留（非回环不替换）", "host=pg-primary.internal port=5432", "pg-primary.internal:5432"},
		{"缺端口 → 不给地址", "host=10.0.0.9 user=repl", ""},
		{"缺主机 → 不给地址", "port=5432 user=repl", ""},
		{"只有无关字段", "user=repl application_name=x", ""},
		{"空串", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parsePGConninfo(tc.in); got != tc.want {
				t.Fatalf("parsePGConninfo(%q) = %q，期望 %q", tc.in, got, tc.want)
			}
		})
	}
}
