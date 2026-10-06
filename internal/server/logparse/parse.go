// Package logparse 从日志文本里提取结构化字段（C2 子批次 D）。
//
// 放在 **Server 侧**而不是 Agent 侧：Agent 已经在每一行上带了命中模式名，
// 若把提字段做在 Agent，就必须重分发全部 Agent 才能让已部署的机器受益；
// 放 Server 侧则现有 Agent 无需升级即可用上字段检索。
//
// 三条硬约束：
//  1. **解析失败绝不丢原文**：提不到字段就是没有字段，行照常落盘——日志的价值首先在原文；
//  2. **名字与值都有界**：名字走 model.IsValidLogFieldName（字符集 + 长度），
//     值有长度上限、每行有字段数上限——否则一条被注入的日志就能把存储与界面撑坏；
//  3. **不把凭据变成可检索字段**：password / token 之类的键**不进字段表**。
//     原文里本来就有，但平台不该再额外提供一个"按口令搜索"的索引。
package logparse

import (
	"encoding/json"
	"io"
	"regexp"
	"strings"

	"github.com/nebula/monitor/internal/model"
)

const (
	// MaxFieldsPerLine 是单行最多提取的字段数。取值依据：常见应用日志的结构化字段在 5~10 个，
	// 16 够用且能把"一条超长 JSON 撑出上千字段"挡在外面。
	MaxFieldsPerLine = 16
	// MaxFieldValueBytes 是单个字段值的长度上限（超出截断并留标记）。
	MaxFieldValueBytes = 256
	// maxJSONDepth 是 JSON 展开层数：顶层 + 一层嵌套用点号连接（如 http.status）。
	// 再深下去字段名会变得又长又碎，检索价值低于噪声。
	maxJSONDepth = 2
)

// sensitiveKeys 是不进字段表的键名（按最后一段小写比对）。
//
// 只挡"明显是凭据"的键名：这不是脱敏（原文照旧落盘），而是不给平台加一条
// "按口令检索"的路径。要彻底不落盘应该改采集侧的脱敏规则，不在这一层解决。
var sensitiveKeys = map[string]bool{
	"password": true, "passwd": true, "pwd": true, "secret": true,
	"token": true, "access_token": true, "refresh_token": true, "api_key": true,
	"apikey": true, "authorization": true, "cookie": true, "set-cookie": true,
	"private_key": true, "privatekey": true, "client_secret": true, "passphrase": true,
	"session": true, "sessionid": true, "session_id": true, "credential": true,
}

// kvPattern 匹配 key=value 形态（无结构日志里最常见的一种）。
//
// 值支持三种写法：双引号、单引号、无引号到空白/逗号/分号为止。
// 无引号分支刻意不允许空格：允许的话 "a=1 and b=2" 会把 "1 and b" 吞成一个值。
var kvPattern = regexp.MustCompile(`(?:^|[\s,;\[\(])([A-Za-z_][A-Za-z0-9_.-]{0,63})=(?:"([^"]{0,256})"|'([^']{0,256})'|([^\s,;\)\]]{1,256}))`)

// Extract 从一行日志里提取字段；返回 nil 表示这行没有可提取的结构。
//
// 先试 JSON（更精确），失败再退到 key=value 扫描：两种形态在同一行里同时出现时，
// JSON 的结果更可信（key=value 扫描会把 JSON 里的字符串内容也扫出来）。
func Extract(text string) map[string]string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}
	if trimmed[0] == '{' {
		if fields := extractJSON(trimmed); len(fields) > 0 {
			return fields
		}
	}
	return extractKeyValue(trimmed)
}

// extractJSON 展开一个 JSON 对象：只取标量（字符串/数字/布尔），嵌套对象用点号连接。
//
// 用 UseNumber 解码：默认的 float64 会把大整数写成 1.2345678901234567e+18，
// 而日志里的 id / traceId 这类值恰恰是长整数，展示错了比不展示更糟。
func extractJSON(text string) map[string]string {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return nil
	}
	// 只接受单个 JSON 对象：后面还有内容说明这行不是纯 JSON（如 `{"a":1} trailing`），
	// 那种行交给 key=value 分支处理更稳妥。
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil
	}
	out := map[string]string{}
	flatten(out, "", raw, 1)
	if len(out) == 0 {
		return nil
	}
	return out
}

func flatten(out map[string]string, prefix string, obj map[string]any, depth int) {
	for key, value := range obj {
		if len(out) >= MaxFieldsPerLine {
			return
		}
		name := key
		if prefix != "" {
			name = prefix + "." + key
		}
		switch v := value.(type) {
		case string:
			addField(out, name, v)
		case json.Number:
			addField(out, name, v.String())
		case bool:
			addField(out, name, boolText(v))
		case map[string]any:
			if depth < maxJSONDepth {
				flatten(out, name, v, depth+1)
			}
		default:
			// 数组 / null 一律跳过：数组展开成什么键名没有共识，
			// 硬编一个（如 a.0）只会产出用户猜不到的字段名。
		}
	}
}

// extractKeyValue 扫描 key=value 对。
func extractKeyValue(text string) map[string]string {
	matches := kvPattern.FindAllStringSubmatch(text, MaxFieldsPerLine)
	if len(matches) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, m := range matches {
		if len(out) >= MaxFieldsPerLine {
			break
		}
		value := m[2]
		if value == "" {
			value = m[3]
		}
		if value == "" {
			value = m[4]
		}
		addField(out, m[1], value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// addField 统一做三件事：名字合法性、凭据键过滤、值长度截断。
// 三处过滤集中在这里，避免 JSON 与 key=value 两条路径各写一遍而漏掉其中之一。
func addField(out map[string]string, name, value string) {
	if len(out) >= MaxFieldsPerLine || !model.IsValidLogFieldName(name) {
		return
	}
	if isSensitive(name) {
		return
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	out[name] = truncateValue(value)
}

// isSensitive 判断键名是否为凭据类：比对**最后一段**（user.password 也要挡住）。
func isSensitive(name string) bool {
	last := name
	if idx := strings.LastIndexAny(name, ".-"); idx >= 0 {
		last = name[idx+1:]
	}
	return sensitiveKeys[strings.ToLower(last)]
}

func truncateValue(v string) string {
	if len(v) <= MaxFieldValueBytes {
		return v
	}
	// 按字节截断可能切在多字节字符中间：退到最后一个完整 UTF-8 边界，避免产出非法字符
	cut := MaxFieldValueBytes
	for cut > 0 && !isUTF8Start(v[cut]) {
		cut--
	}
	return v[:cut] + "…"
}

func isUTF8Start(b byte) bool { return b&0xC0 != 0x80 }

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
