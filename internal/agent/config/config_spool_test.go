package config

import "testing"

// 上报磁盘缓冲的默认值与"0 表示关闭"的语义。
//
// 这条容易被改坏：把 `0` 当成"没配"就会让运维的关闭动作静默失效，
// 而这台机器上就会继续悄悄写磁盘。
func TestSpoolDefaults(t *testing.T) {
	cases := []struct {
		name     string
		file     string
		maxMB    *int
		wantFile string
		wantMB   int64
	}{
		{name: "都留空取默认", file: "", maxMB: nil, wantFile: DefaultSpoolFile, wantMB: int64(DefaultSpoolMaxMB) << 20},
		{name: "显式 0 表示关闭", file: "", maxMB: intPtr(0), wantFile: DefaultSpoolFile, wantMB: 0},
		{name: "自定义上限", file: "/tmp/x.jsonl", maxMB: intPtr(2), wantFile: "/tmp/x.jsonl", wantMB: 2 << 20},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{SpoolFile: tc.file, SpoolMaxMB: tc.maxMB}
			// 直接调用与 Load 里相同的归一化逻辑：单独抽出来只为了能这样测
			normalizeSpool(cfg)
			if cfg.SpoolFile != tc.wantFile {
				t.Fatalf("缓冲路径 = %q，期望 %q", cfg.SpoolFile, tc.wantFile)
			}
			if got := cfg.SpoolMaxBytes(); got != tc.wantMB {
				t.Fatalf("缓冲上限 = %d 字节，期望 %d", got, tc.wantMB)
			}
		})
	}
}

func intPtr(v int) *int { return &v }
