package alert

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/config"
)

// Notifier 通知渠道接口。
type Notifier interface {
	Notify(e model.AlertEvent) error
	Channel() string
}

// EmailNotifier 邮件通知（net/smtp，无额外依赖）。
type EmailNotifier struct {
	cfg config.EmailConfig
}

// NewEmailNotifier 创建邮件通知器。
func NewEmailNotifier(cfg config.EmailConfig) *EmailNotifier {
	return &EmailNotifier{cfg: cfg}
}

// Channel 返回渠道名。
func (n *EmailNotifier) Channel() string { return "email" }

// Notify 发送告警邮件。
func (n *EmailNotifier) Notify(e model.AlertEvent) error {
	if !n.cfg.Enabled || len(n.cfg.To) == 0 {
		return nil
	}
	subject := fmt.Sprintf("[监控告警][%s] %s", e.Severity, e.RuleName)
	body := fmt.Sprintf("告警事件\n节点: %s\n规则: %s\n指标: %s\n触发值: %.2f %s 阈值 %.2f\n状态: %s\n时间: %s\n描述: %s\n",
		e.Node, e.RuleName, e.Metric, e.Value, e.Operator, e.Threshold, e.State, timeStr(e.StartsAt), e.Message)

	msg := bytes.Buffer{}
	msg.WriteString("From: " + n.cfg.From + "\r\n")
	msg.WriteString("To: ")
	for i, t := range n.cfg.To {
		if i > 0 {
			msg.WriteString(", ")
		}
		msg.WriteString(t)
	}
	msg.WriteString("\r\n")
	msg.WriteString("Subject: " + subject + "\r\n")
	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	msg.WriteString(body)

	addr := fmt.Sprintf("%s:%d", n.cfg.SMTPHost, n.cfg.SMTPPort)
	var auth smtp.Auth
	if n.cfg.Username != "" {
		auth = smtp.PlainAuth("", n.cfg.Username, n.cfg.Password, n.cfg.SMTPHost)
	}
	useStartTLS := n.cfg.UseStartTLS || (n.cfg.UseTLS && n.cfg.SMTPPort == 587)
	useImplicitTLS := n.cfg.UseTLS && !useStartTLS

	var err error
	switch {
	case useImplicitTLS:
		err = sendTLS(addr, n.cfg.SMTPHost, auth, n.cfg.From, n.cfg.To, msg.Bytes())
	case useStartTLS:
		err = sendStartTLS(addr, n.cfg.SMTPHost, auth, n.cfg.From, n.cfg.To, msg.Bytes())
	default:
		err = smtp.SendMail(addr, auth, n.cfg.From, n.cfg.To, msg.Bytes())
	}
	if err != nil {
		slog.Error("发送告警邮件失败", "err", err)
		return err
	}
	return nil
}

// sendTLS 通过显式 TLS 发送邮件（隐式 TLS，端口通常 465）。
func sendTLS(addr, host string, auth smtp.Auth, from string, to []string, msg []byte) error {
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host})
	if err != nil {
		return err
	}
	defer conn.Close()
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer c.Quit()
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return err
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, t := range to {
		if err := c.Rcpt(t); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	_, err = w.Write(msg)
	if err != nil {
		return err
	}
	return w.Close()
}

// sendStartTLS 先建立明文连接，再通过 STARTTLS 升级加密发送（端口通常 587）。
func sendStartTLS(addr, host string, auth smtp.Auth, from string, to []string, msg []byte) error {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer c.Quit()
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return err
		}
	}
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return err
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, t := range to {
		if err := c.Rcpt(t); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	_, err = w.Write(msg)
	if err != nil {
		return err
	}
	return w.Close()
}

// WebhookNotifier Webhook 通知（POST JSON）。
type WebhookNotifier struct {
	cfg config.WebhookConfig
}

// NewWebhookNotifier 创建 Webhook 通知器。
func NewWebhookNotifier(cfg config.WebhookConfig) *WebhookNotifier {
	return &WebhookNotifier{cfg: cfg}
}

// Channel 返回渠道名。
func (n *WebhookNotifier) Channel() string { return "webhook" }

// Notify 向配置的 Webhook URL 发送告警 JSON。
func (n *WebhookNotifier) Notify(e model.AlertEvent) error {
	if !n.cfg.Enabled || len(n.cfg.URLs) == 0 {
		return nil
	}
	payload, err := json.Marshal(e)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	for _, u := range n.cfg.URLs {
		resp, err := client.Post(u, "application/json", bytes.NewReader(payload))
		if err != nil {
			slog.Error("Webhook 通知失败", "url", u, "err", err)
			continue
		}
		resp.Body.Close()
	}
	return nil
}

// BuildNotifiers 根据配置构建通知器列表。
func BuildNotifiers(cfg config.NotifyConfig) []Notifier {
	var ns []Notifier
	if cfg.Email.Enabled {
		ns = append(ns, NewEmailNotifier(cfg.Email))
	}
	if cfg.Webhook.Enabled {
		ns = append(ns, NewWebhookNotifier(cfg.Webhook))
	}
	if cfg.DingTalk.Enabled {
		ns = append(ns, NewDingTalkNotifier(cfg.DingTalk))
	}
	if cfg.Feishu.Enabled {
		ns = append(ns, NewFeishuNotifier(cfg.Feishu))
	}
	if cfg.WeCom.Enabled {
		ns = append(ns, NewWeComNotifier(cfg.WeCom))
	}
	return ns
}

func timeStr(ms int64) string {
	if ms == 0 {
		return ""
	}
	return time.UnixMilli(ms).Format("2006-01-02 15:04:05")
}

// ---- 钉钉 / 飞书 / 企业微信 通知 ----

// alertText 生成纯文本告警内容（飞书 / 企业微信 text 类型使用）。
func alertText(e model.AlertEvent) string {
	return fmt.Sprintf("[监控告警] 级别:%s 状态:%s\n节点: %s\n规则: %s\n指标: %s\n触发值: %.2f %s 阈值 %.2f\n时间: %s\n描述: %s",
		e.Severity, e.State, e.Node, e.RuleName, e.Metric, e.Value, e.Operator, e.Threshold, timeStr(e.StartsAt), e.Message)
}

// alertMarkdown 生成钉钉 markdown 告警内容。
func alertMarkdown(e model.AlertEvent) string {
	return fmt.Sprintf("### 监控告警\n> **级别**: %s  **状态**: %s\n> **节点**: %s\n> **规则**: %s\n> **指标**: %s\n> **触发值**: %.2f %s 阈值 %.2f\n> **时间**: %s\n> **描述**: %s",
		e.Severity, e.State, e.Node, e.RuleName, e.Metric, e.Value, e.Operator, e.Threshold, timeStr(e.StartsAt), e.Message)
}

// postJSON 向指定 URL POST JSON，并校验响应状态。
func postJSON(rawURL string, body interface{}) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(rawURL, "application/json", bytes.NewReader(data))
	if err != nil {
		slog.Error("通知渠道请求失败", "err", err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		slog.Error("通知渠道返回非成功状态", "status", resp.StatusCode, "body", string(b))
		return fmt.Errorf("通知渠道返回 %d", resp.StatusCode)
	}
	return nil
}

// DingTalkNotifier 钉钉机器人（支持加签与 @）。
type DingTalkNotifier struct {
	cfg config.DingTalkConfig
}

func NewDingTalkNotifier(cfg config.DingTalkConfig) *DingTalkNotifier {
	return &DingTalkNotifier{cfg: cfg}
}

func (n *DingTalkNotifier) Channel() string { return "dingtalk" }

func (n *DingTalkNotifier) Notify(e model.AlertEvent) error {
	if !n.cfg.Enabled || len(n.cfg.URLs) == 0 {
		return nil
	}
	var ts int64
	var sign string
	if n.cfg.Secret != "" {
		var err error
		ts, sign, err = dingSign(n.cfg.Secret)
		if err != nil {
			return err
		}
	}
	at := map[string]interface{}{}
	if len(n.cfg.AtMobiles) > 0 {
		at["atMobiles"] = n.cfg.AtMobiles
	}
	for _, base := range n.cfg.URLs {
		u := base
		if n.cfg.Secret != "" {
			u = fmt.Sprintf("%s&timestamp=%d&sign=%s", u, ts, url.QueryEscape(sign))
		}
		payload := map[string]interface{}{
			"msgtype": "markdown",
			"markdown": map[string]string{
				"title": "监控告警",
				"text":  alertMarkdown(e),
			},
		}
		if len(at) > 0 {
			payload["at"] = at
		}
		if err := postJSON(u, payload); err != nil {
			return err
		}
	}
	return nil
}

// dingSign 钉钉加签：HMAC-SHA256 → base64，timestamp 为毫秒。
func dingSign(secret string) (int64, string, error) {
	ts := time.Now().UnixMilli()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(fmt.Sprintf("%d\n%s", ts, secret)))
	return ts, base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}

// FeishuNotifier 飞书机器人（支持签名）。
type FeishuNotifier struct {
	cfg config.FeishuConfig
}

func NewFeishuNotifier(cfg config.FeishuConfig) *FeishuNotifier {
	return &FeishuNotifier{cfg: cfg}
}

func (n *FeishuNotifier) Channel() string { return "feishu" }

func (n *FeishuNotifier) Notify(e model.AlertEvent) error {
	if !n.cfg.Enabled || len(n.cfg.URLs) == 0 {
		return nil
	}
	var ts int64
	var sign string
	if n.cfg.Secret != "" {
		ts, sign = feishuSign(n.cfg.Secret)
	}
	for _, u := range n.cfg.URLs {
		body := map[string]interface{}{
			"msg_type": "text",
			"content":  map[string]string{"text": alertText(e)},
		}
		if n.cfg.Secret != "" {
			body["timestamp"] = ts
			body["sign"] = sign
		}
		if err := postJSON(u, body); err != nil {
			return err
		}
	}
	return nil
}

// feishuSign 飞书签名：HMAC-SHA256 → base64，timestamp 为秒。
func feishuSign(secret string) (int64, string) {
	ts := time.Now().Unix()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(fmt.Sprintf("%d\n%s", ts, secret)))
	return ts, base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// WeComNotifier 企业微信机器人（支持 @）。
type WeComNotifier struct {
	cfg config.WeComConfig
}

func NewWeComNotifier(cfg config.WeComConfig) *WeComNotifier {
	return &WeComNotifier{cfg: cfg}
}

func (n *WeComNotifier) Channel() string { return "wecom" }

func (n *WeComNotifier) Notify(e model.AlertEvent) error {
	if !n.cfg.Enabled || len(n.cfg.URLs) == 0 {
		return nil
	}
	content := map[string]interface{}{"content": alertText(e)}
	if len(n.cfg.MentionedList) > 0 {
		content["mentioned_list"] = n.cfg.MentionedList
	}
	for _, u := range n.cfg.URLs {
		body := map[string]interface{}{
			"msgtype": "text",
			"content": content,
		}
		if err := postJSON(u, body); err != nil {
			return err
		}
	}
	return nil
}
