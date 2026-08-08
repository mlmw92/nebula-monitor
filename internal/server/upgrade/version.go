package upgrade

import (
	"fmt"
	"strconv"
	"strings"
)

type semanticVersion struct {
	major, minor, patch int
	pre                 string
}

func parseSemanticVersion(raw string) (semanticVersion, error) {
	v := strings.TrimSpace(strings.TrimPrefix(raw, "v"))
	if v == "" {
		return semanticVersion{}, fmt.Errorf("版本号不能为空")
	}
	if plus := strings.IndexByte(v, '+'); plus >= 0 {
		v = v[:plus]
	}
	parts := strings.SplitN(v, "-", 2)
	core := strings.Split(parts[0], ".")
	if len(core) != 3 {
		return semanticVersion{}, fmt.Errorf("版本号 %q 不是合法 SemVer", raw)
	}
	vals := [3]int{}
	for i := range core {
		n, err := strconv.Atoi(core[i])
		if err != nil || n < 0 {
			return semanticVersion{}, fmt.Errorf("版本号 %q 不是合法 SemVer", raw)
		}
		vals[i] = n
	}
	if len(parts) == 2 && parts[1] == "" {
		return semanticVersion{}, fmt.Errorf("版本号 %q 不是合法 SemVer", raw)
	}
	return semanticVersion{major: vals[0], minor: vals[1], patch: vals[2], pre: func() string {
		if len(parts) == 2 {
			return parts[1]
		}
		return ""
	}()}, nil
}

func compareSemanticVersion(a, b semanticVersion) int {
	for _, pair := range [][2]int{{a.major, b.major}, {a.minor, b.minor}, {a.patch, b.patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if a.pre == b.pre {
		return 0
	}
	if a.pre == "" {
		return 1
	}
	if b.pre == "" {
		return -1
	}
	return strings.Compare(a.pre, b.pre)
}

// validateVersionForUpload 校验普通升级目标版本和最低兼容版本。
// dev/空当前版本用于开发环境，此时只校验目标版本格式。
func validateVersionForUpload(current, target, previousMin string) error {
	t, err := parseSemanticVersion(target)
	if err != nil {
		return err
	}
	if current == "" || current == "dev" {
		return nil
	}
	cur, err := parseSemanticVersion(current)
	if err != nil {
		return fmt.Errorf("当前版本无效: %w", err)
	}
	if previousMin != "" {
		min, err := parseSemanticVersion(previousMin)
		if err != nil {
			return fmt.Errorf("最低兼容版本无效: %w", err)
		}
		if compareSemanticVersion(cur, min) < 0 {
			return fmt.Errorf("当前版本 %s 低于升级包要求的最低版本 %s", current, previousMin)
		}
	}
	if compareSemanticVersion(t, cur) <= 0 {
		return fmt.Errorf("目标版本 %s 必须高于当前版本 %s；降级请使用回退功能", target, current)
	}
	return nil
}
