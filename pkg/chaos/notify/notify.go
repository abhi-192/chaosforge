package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

type Message struct {
	Type   string            `json:"type"`
	Env    string            `json:"env"`
	RunID  string            `json:"runId"`
	Text   string            `json:"text"`
	Fields map[string]string `json:"fields,omitempty"`
	At     time.Time         `json:"at"`
}

type Notifier interface {
	Notify(ctx context.Context, m Message) error
}

type noop struct{}

func (noop) Notify(context.Context, Message) error { return nil }

type fanout struct{ ns []Notifier }

func (f fanout) Notify(ctx context.Context, m Message) error {
	var firstErr error
	for _, n := range f.ns {
		if err := n.Notify(ctx, m); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// NewFromEnv returns a notifier based on environment variables.
// All are optional; if none are set, a no-op notifier is returned.
//
// - CHAOS_NOTIFY_SNS_TOPIC_ARN: publish JSON message to SNS
// - CHAOS_NOTIFY_SLACK_WEBHOOK_URL: post a Slack webhook payload
// - CHAOS_NOTIFY_SLACK_WEBHOOK_PARAM: SSM parameter name to read webhook URL (SecureString supported)
func NewFromEnv(cfg aws.Config, ssmc *ssm.Client) (Notifier, error) {
	var ns []Notifier
	if topic := strings.TrimSpace(os.Getenv("CHAOS_NOTIFY_SNS_TOPIC_ARN")); topic != "" {
		ns = append(ns, &snsNotifier{c: sns.NewFromConfig(cfg), topicArn: topic})
	}

	webhook := strings.TrimSpace(os.Getenv("CHAOS_NOTIFY_SLACK_WEBHOOK_URL"))
	if webhook == "" {
		param := strings.TrimSpace(os.Getenv("CHAOS_NOTIFY_SLACK_WEBHOOK_PARAM"))
		if param != "" {
			if ssmc == nil {
				ssmc = ssm.NewFromConfig(cfg)
			}
			out, err := ssmc.GetParameter(context.Background(), &ssm.GetParameterInput{Name: aws.String(param), WithDecryption: aws.Bool(true)})
			if err != nil {
				return nil, fmt.Errorf("read slack webhook from ssm: %w", err)
			}
			webhook = aws.ToString(out.Parameter.Value)
		}
	}
	if webhook != "" {
		ns = append(ns, &slackNotifier{webhookURL: webhook, httpc: &http.Client{Timeout: 5 * time.Second}})
	}

	if len(ns) == 0 {
		return noop{}, nil
	}
	if len(ns) == 1 {
		return ns[0], nil
	}
	return fanout{ns: ns}, nil
}

type snsNotifier struct {
	c        *sns.Client
	topicArn string
}

func (n *snsNotifier) Notify(ctx context.Context, m Message) error {
	if m.At.IsZero() {
		m.At = time.Now().UTC()
	}
	b, _ := json.Marshal(m)
	_, err := n.c.Publish(ctx, &sns.PublishInput{
		TopicArn: aws.String(n.topicArn),
		Message:  aws.String(string(b)),
		Subject:  aws.String(fmt.Sprintf("ChaosForge %s %s", m.Type, m.RunID)),
	})
	return err
}

type slackNotifier struct {
	webhookURL string
	httpc      *http.Client
}

func (s *slackNotifier) Notify(ctx context.Context, m Message) error {
	if m.At.IsZero() {
		m.At = time.Now().UTC()
	}
	// Slack incoming webhook supports a simple payload with "text".
	text := m.Text
	if text == "" {
		text = fmt.Sprintf("ChaosForge %s: %s (%s)", m.Type, m.RunID, m.Env)
	}
	if len(m.Fields) > 0 {
		var b strings.Builder
		b.WriteString(text)
		for k, v := range m.Fields {
			b.WriteString("\n")
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(v)
		}
		text = b.String()
	}
	payload := map[string]any{"text": text}
	b, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.webhookURL, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("slack webhook status=%d", resp.StatusCode)
	}
	return nil
}
