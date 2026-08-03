package collector

import (
	"regexp"
	"testing"
)

func TestNginxLogReCombinedTimed(t *testing.T) {
	lines := []string{
		`127.0.0.1 - - [03/Aug/2026:22:11:35 +0800] "GET /nginx_status HTTP/1.1" 200 109 0.000`,
		`34.141.72.86 - - [03/Aug/2026:22:14:28 +0800] "GET /status HTTP/1.1" 200 87 0.000`,
		`120.220.79.14 - - [03/Aug/2026:22:14:28 +0800] "GET /status HTTP/1.1" 200 88 0.000`,
		`8.209.136.9 - - [03/Aug/2026:22:14:28 +0800] "GET /status HTTP/1.1" 200 86 0.000`,
	}
	re := regexp.MustCompile(`^(\S+) - - \[([^\]]+)\] "([^"]*)" (\d{3}) (\d+)(?:\s+(\S+))?$`)
	for _, line := range lines {
		m := re.FindStringSubmatch(line)
		if m == nil {
			t.Errorf("regexp 匹配失败: %s", line)
		} else {
			t.Logf("匹配成功: addr=%s time=%s request=%s status=%s body=%s reqtime=%s",
				m[1], m[2], m[3], m[4], m[5], m[6])
		}
	}
}