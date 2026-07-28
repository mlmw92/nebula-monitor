package alert

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/smtp"
	"net/http"
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
	var err error
	if n.cfg.UseTLS {
		err = sendTLS(addr, n.cfg.SMTPHost, auth, n.cfg.From, n.cfg.To, msg.Bytes())
	} else {
		err = smtp.SendMail(addr, auth, n.cfg.From, n.cfg.To, msg.Bytes())
	}
	if err != nil {
		slog.Error("发送告警邮件失败", "err", err)
		return err
	}
	return nil
}

// sendTLS 通过显式 TLS 发送邮件。
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
	return ns
}

func timeStr(ms int64) string {
	if ms == 0 {
		return ""
	}
	return time.UnixMilli(ms).Format("2006-01-02 15:04:05")
}
