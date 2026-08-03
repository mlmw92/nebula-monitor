package collector

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"sort"
	"testing"
)

// 真实 access.log 诊断：复用 agent 的 getParser/parse，读取整份文件，
// 统计匹配率并打印未匹配样例，绕开 Shell 对正则里 $ 的转义干扰。
var (
	realLogPath   = flag.String("real-log-path", "", "真实 access.log 路径，留空则跳过")
	realLogFormat = flag.String("real-log-format", "combined_timed", "日志格式名")
	realLogAddr   = flag.String("real-log-addr", "127.0.0.1:80", "实例 addr（仅用于输出展示）")
)

func TestRealNginxLog(t *testing.T) {
	if *realLogPath == "" {
		t.Skip("未设置 -real-log-path，跳过真实日志诊断")
	}
	f, err := os.Open(*realLogPath)
	if err != nil {
		t.Fatalf("打开日志失败 %s: %v", *realLogPath, err)
	}
	defer f.Close()

	parsers := nginxLogParserList(*realLogFormat)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)

	var total, matched int
	var unmatched []string
	ipStats := map[string]*ipAgg{}
	statusCount := map[string]float64{}
	uriCount := map[string]float64{}
	var totalRequests, totalBytes, latencySum, latencyN float64

	for sc.Scan() {
		line := sc.Text()
		total++
		var m []string
		var used *nginxLogParser
		for _, p := range parsers {
			if mm := p.re.FindStringSubmatch(line); mm != nil {
				m = mm
				used = p
				break
			}
		}
		if m == nil {
			if len(unmatched) < 10 {
				unmatched = append(unmatched, fmt.Sprintf("[行 %d] %q", total, line))
			}
			continue
		}
		matched++
		used.parse(m, ipStats, statusCount, uriCount, &totalRequests, &totalBytes, &latencySum, &latencyN)
	}

	fmt.Printf("\n=== Nginx access.log 诊断 ===\n")
	fmt.Printf("文件: %s\n格式: %s\n实例: %s\n", *realLogPath, *realLogFormat, *realLogAddr)
		fmt.Printf("总行数: %d  匹配: %d  未匹配: %d  匹配率: %.1f%%\n",
		total, matched, total-matched, pct(matched, total))
	fmt.Printf("聚合: totalRequests=%v totalBytes=%v avgLatency=%v\n",
		totalRequests, totalBytes, round2(div(latencySum, latencyN)))
	fmt.Printf("状态码分布: %v\n", statusCount)
	fmt.Printf("Top URI: %v\n", topNString(uriCount, 10))
	fmt.Printf("Top IP:  %v\n", topNIP(ipStats, 10))
	if len(unmatched) > 0 {
		fmt.Printf("\n--- 未匹配样例（前 %d 行，用于核对日志格式是否与正则一致）---\n", len(unmatched))
		for _, u := range unmatched {
			fmt.Println(u)
		}
	}
	fmt.Printf("=== 诊断结束 ===\n")
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b) * 100
}

func div(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a / b
}

func topNString(m map[string]float64, n int) []string {
	type kv struct {
		k string
		v float64
	}
	ss := make([]kv, 0, len(m))
	for k, v := range m {
		ss = append(ss, kv{k, v})
	}
	sort.Slice(ss, func(i, j int) bool { return ss[i].v > ss[j].v })
	if len(ss) > n {
		ss = ss[:n]
	}
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, fmt.Sprintf("%s=%v", s.k, s.v))
	}
	return out
}

func topNIP(m map[string]*ipAgg, n int) []string {
	type kv struct {
		k string
		v float64
	}
	ss := make([]kv, 0, len(m))
	for k, v := range m {
		ss = append(ss, kv{k, v.requests})
	}
	sort.Slice(ss, func(i, j int) bool { return ss[i].v > ss[j].v })
	if len(ss) > n {
		ss = ss[:n]
	}
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, fmt.Sprintf("%s=%v", s.k, s.v))
	}
	return out
}
