// Package alert 在存储提交成功后发布规则与状态，避免失败写入造成通知与状态分离。
package alert

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"text/template"
	"unicode/utf8"

	"github.com/xjetry/probe/internal/hub/store"
)

var ErrInvalid = errors.New("invalid")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}
func checkName(name string) error {
	if n := utf8.RuneCountInString(name); n < 1 || n > 64 {
		return invalid("name must contain between 1 and 64 characters")
	}
	return nil
}

func CheckRule(r store.AlertRule) error {
	if err := checkName(r.Name); err != nil {
		return err
	}
	if !r.AllNodes && len(r.NodeIDs) == 0 {
		return invalid("node_ids must not be empty unless all_nodes is true")
	}
	switch r.Kind {
	case store.KindOffline:
		return nil
	case store.KindProbe:
		if r.TaskID == 0 {
			return invalid("task_id must not be 0")
		}
		if math.IsNaN(r.Threshold) || math.IsInf(r.Threshold, 0) {
			return invalid("threshold must be finite")
		}
		switch r.Metric {
		case store.MetricLossPct:
			if r.Threshold < 0 || r.Threshold > 100 {
				return invalid("threshold must be between 0 and 100 for loss_pct")
			}
		case store.MetricRttMs:
			if r.Threshold <= 0 {
				return invalid("threshold must be greater than 0 for rtt_ms")
			}
		default:
			return invalid("metric must be loss_pct or rtt_ms")
		}
		if r.ForMinutes < 1 || r.ForMinutes > 60 {
			return invalid("for_minutes must be between 1 and 60")
		}
		return nil
	default:
		return invalid("kind must be offline or probe")
	}
}

type telegramConfig struct {
	BotToken string `json:"bot_token"`
	ChatID   string `json:"chat_id"`
}
type webhookConfig struct {
	URL          string            `json:"url"`
	Method       string            `json:"method"`
	Headers      map[string]string `json:"headers"`
	BodyTemplate string            `json:"body_template"`
}

const defaultBodyTemplate = `{"rule":{{json .Rule}},"node":{{json .Node}},"kind":{{json .Kind}},"transition":{{json .Transition}},"value":{{.Value}},"at":{{.At.Unix}},"summary":{{json .Summary}}}`

func parseBodyTemplate(body string) (*template.Template, error) {
	if body == "" {
		body = defaultBodyTemplate
	}
	return template.New("body").Funcs(template.FuncMap{"json": func(s string) string { b, _ := json.Marshal(s); return string(b) }}).Parse(body)
}

func httpToken(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c)) {
			return false
		}
	}
	return true
}

func CheckChannel(c store.NotifyChannel) error {
	if err := checkName(c.Name); err != nil {
		return err
	}
	switch c.Kind {
	case store.ChannelTelegram:
		var cfg telegramConfig
		if err := json.Unmarshal([]byte(c.Config), &cfg); err != nil {
			return invalid("config must be a telegram JSON object: %v", err)
		}
		if cfg.ChatID == "" {
			return invalid("chat_id must not be empty")
		}
		if cfg.BotToken == "" {
			return invalid("bot_token must not be empty")
		}
	case store.ChannelWebhook:
		var cfg webhookConfig
		if err := json.Unmarshal([]byte(c.Config), &cfg); err != nil {
			return invalid("config must be a webhook JSON object: %v", err)
		}
		u, err := url.Parse(cfg.URL)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return invalid("url must be an absolute http or https URL")
		}
		if cfg.Method != "" && cfg.Method != "POST" && cfg.Method != "PUT" && cfg.Method != "PATCH" {
			return invalid("method must be POST, PUT or PATCH (empty defaults to POST)")
		}
		if len(cfg.Headers) > 16 {
			return invalid("headers must contain at most 16 entries")
		}
		for key := range cfg.Headers {
			if !httpToken(key) {
				return invalid("headers key %q must be an HTTP token", key)
			}
		}
		if _, err := parseBodyTemplate(cfg.BodyTemplate); err != nil {
			return invalid("body_template must be a valid Go text/template: %v", err)
		}
	default:
		return invalid("kind must be telegram or webhook")
	}
	return nil
}
