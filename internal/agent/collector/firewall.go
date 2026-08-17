package collector

import (
	"os/exec"
	"strings"

	"github.com/nebula/monitor/internal/model"
)

// collectFirewallRules 采集主机防火墙规则列表，自动探测后端：
//   - firewalld (firewall-cmd --list-all --zone=*)
//   - iptables (iptables -L -n -v --line-numbers)
//   - nftables (nft list ruleset)
//   - ufw (ufw status numbered)
//
// 返回 FirewallRule 列表。若所有后端均不可用则返回空切片。
func collectFirewallRules() []model.FirewallRule {
	var rules []model.FirewallRule

	// 优先尝试 firewalld
	if r := collectFirewalld(); len(r) > 0 {
		rules = append(rules, r...)
	} else if r := collectIptables(); len(r) > 0 {
		rules = append(rules, r...)
	} else if r := collectNftables(); len(r) > 0 {
		rules = append(rules, r...)
	} else if r := collectUfw(); len(r) > 0 {
		rules = append(rules, r...)
	}

	if rules == nil {
		return []model.FirewallRule{}
	}
	return rules
}

// ---- firewalld ----

func collectFirewalld() []model.FirewallRule {
	path, err := exec.LookPath("firewall-cmd")
	if err != nil {
		return nil
	}
	out, err := exec.Command(path, "--state").Output()
	if err != nil || strings.TrimSpace(string(out)) != "running" {
		return nil
	}

	// 获取所有 zone
	zonesOut, err := exec.Command(path, "--get-zones").Output()
	if err != nil {
		return nil
	}
	zones := strings.Fields(strings.TrimSpace(string(zonesOut)))

	var rules []model.FirewallRule
	for _, zone := range zones {
		allOut, err := exec.Command(path, "--list-all", "--zone="+zone).Output()
		if err != nil {
			continue
		}
		lines := strings.Split(strings.TrimSpace(string(allOut)), "\n")
		for i, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			// 解析 services / ports / rich rules 等行
			if strings.HasPrefix(line, "services:") {
				services := strings.Split(line[len("services:"):], ",")
				for _, svc := range services {
					svc = strings.TrimSpace(svc)
					if svc == "" {
						continue
					}
					rules = append(rules, model.FirewallRule{
						Backend: "firewalld",
						Chain:   zone,
						RuleNum: i,
						Action:  "ACCEPT",
						DstPort: svc,
						Options: line,
					})
				}
			} else if strings.HasPrefix(line, "ports:") {
				ports := strings.Split(line[len("ports:"):], ",")
				for _, p := range ports {
					p = strings.TrimSpace(p)
					if p == "" {
						continue
					}
					rules = append(rules, model.FirewallRule{
						Backend: "firewalld",
						Chain:   zone,
						RuleNum: i,
						Action:  "ACCEPT",
						DstPort: p,
						Options: line,
					})
				}
			} else if strings.HasPrefix(line, "rich rules:") {
				ruleText := strings.TrimPrefix(line, "rich rules:")
				rules = append(rules, model.FirewallRule{
					Backend: "firewalld",
					Chain:   zone,
					RuleNum: i,
					Action:  "RICH",
					Options: strings.TrimSpace(ruleText),
				})
			}
		}
	}
	return rules
}

// ---- iptables ----

func collectIptables() []model.FirewallRule {
	path, err := exec.LookPath("iptables")
	if err != nil {
		return nil
	}
	out, err := exec.Command(path, "-L", "-n", "-v", "--line-numbers").Output()
	if err != nil {
		return nil
	}
	return parseIptablesOutput(string(out), "iptables")
}

func parseIptablesOutput(output, backend string) []model.FirewallRule {
	lines := strings.Split(output, "\n")
	var rules []model.FirewallRule
	currentChain := ""

	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// 链头行：Chain INPUT (policy ACCEPT)
		if strings.HasPrefix(line, "Chain ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				currentChain = parts[1]
			}
			continue
		}
		// 表头行：target prot opt in out source destination
		if strings.HasPrefix(line, "target") && strings.Contains(line, "prot") {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) < 8 {
			continue
		}
		action := parts[0]
		protocol := parts[1]
		srcAddr := parts[7]
		dstPort := ""
		if len(parts) > 9 {
			dstPort = parts[9]
			// 去掉 dpts: 前缀
			if strings.HasPrefix(dstPort, "dpts:") {
				dstPort = strings.TrimPrefix(dstPort, "dpts:")
			} else if strings.HasPrefix(dstPort, "dpt:") {
				dstPort = strings.TrimPrefix(dstPort, "dpt:")
			}
		}

		rules = append(rules, model.FirewallRule{
			Backend:  backend,
			Chain:    currentChain,
			RuleNum:  i,
			Action:   action,
			Protocol: protocol,
			SrcAddr:  srcAddr,
			DstPort:  dstPort,
			Options:  line,
		})
	}
	return rules
}

// ---- nftables ----

func collectNftables() []model.FirewallRule {
	path, err := exec.LookPath("nft")
	if err != nil {
		return nil
	}
	out, err := exec.Command(path, "list", "ruleset").Output()
	if err != nil {
		return nil
	}
	return parseNftOutput(string(out))
}

func parseNftOutput(output string) []model.FirewallRule {
	lines := strings.Split(output, "\n")
	var rules []model.FirewallRule
	ruleNum := 0
	currentTable := ""
	currentChain := ""

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// table inet filter { 或 table ip filter {
		if strings.HasPrefix(line, "table ") {
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				currentTable = parts[2]
			}
			continue
		}
		// chain input { 或 chain output {
		if strings.HasPrefix(line, "chain ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				currentChain = strings.TrimSuffix(parts[1], "{")
				currentChain = strings.TrimSpace(currentChain)
			}
			continue
		}
		// 规则行
		if !strings.HasPrefix(line, "}") && !strings.HasPrefix(line, "chain ") &&
			!strings.HasPrefix(line, "table ") {
			action := ""
			protocol := ""
			dport := ""

			lower := strings.ToLower(line)
			if strings.Contains(lower, "accept") {
				action = "ACCEPT"
			} else if strings.Contains(lower, "drop") {
				action = "DROP"
			} else if strings.Contains(lower, "reject") {
				action = "REJECT"
			}

			if strings.Contains(lower, "tcp") {
				protocol = "tcp"
			} else if strings.Contains(lower, "udp") {
				protocol = "udp"
			}

			// 提取 dport
			if idx := strings.Index(lower, "dport"); idx >= 0 {
				sub := lower[idx:]
				fields := strings.Fields(sub)
				if len(fields) >= 2 {
					dport = fields[1]
				}
			}

			rules = append(rules, model.FirewallRule{
				Backend:  "nftables",
				Chain:    currentTable + "/" + currentChain,
				RuleNum:  ruleNum,
				Action:   action,
				Protocol: protocol,
				DstPort:  dport,
				Options:  line,
			})
			ruleNum++
		}
	}
	return rules
}

// ---- ufw ----

func collectUfw() []model.FirewallRule {
	path, err := exec.LookPath("ufw")
	if err != nil {
		return nil
	}
	out, err := exec.Command(path, "status", "numbered").Output()
	if err != nil {
		return nil
	}
	return parseUfwOutput(string(out))
}

func parseUfwOutput(output string) []model.FirewallRule {
	lines := strings.Split(output, "\n")
	var rules []model.FirewallRule

	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Status: active / inactive
		if strings.HasPrefix(line, "Status:") {
			continue
		}
		// 规则行格式: [ 1] 22/tcp ALLOW IN Anywhere
		if !strings.HasPrefix(line, "[") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 5 {
			continue
		}
		// [ 1] 22/tcp ALLOW IN Anywhere
		portProto := parts[1] // 22/tcp
		action := parts[2]    // ALLOW/DENY
		direction := parts[3] // IN/OUT
		srcAddr := ""
		if len(parts) > 4 {
			srcAddr = parts[4] // Anywhere / 具体IP
		}

		proto := ""
		pp := strings.Split(portProto, "/")
		dport := pp[0]
		if len(pp) > 1 {
			proto = strings.ToUpper(pp[1])
		}

		rules = append(rules, model.FirewallRule{
			Backend:  "ufw",
			Chain:    direction,
			RuleNum:  i,
			Action:   action,
			Protocol: proto,
			SrcAddr:  srcAddr,
			DstPort:  dport,
			Options:  line,
		})
	}
	return rules
}
