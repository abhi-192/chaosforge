package main

import (
	"context"
	"time"

	"chaosforge/pkg/chaos/lambdautil"
	"chaosforge/pkg/chaos/model"
	"chaosforge/pkg/chaos/util"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/rs/zerolog/log"
)

type input struct {
	RunID string `json:"runId"`
}

type output struct {
	Done           bool     `json:"done"`
	StopAlarmFired bool     `json:"stopAlarmFired"`
	StopAlarms     []string `json:"stopAlarms,omitempty"`
	Message        string   `json:"message"`
}

type handler struct {
	store modelStore
	cw    *cloudwatch.Client
}

type modelStore interface {
	GetRun(context.Context, string) (*model.ChaosRun, error)
	GetTemplate(context.Context, string, int) (*model.ChaosTemplate, error)
	PutEvent(context.Context, *model.ChaosRunEvent) error
}

func main() {
	util.InitLogger("monitor")
	cfg, err := lambdautil.LoadConfig(context.Background())
	if err != nil {
		panic(err)
	}
	st := lambdautil.NewStoreFromEnv(cfg)
	h := &handler{store: st, cw: cloudwatch.NewFromConfig(cfg)}
	lambda.Start(h.handle)
}

func (h *handler) handle(ctx context.Context, in input) (output, error) {
	r, err := h.store.GetRun(ctx, in.RunID)
	if err != nil {
		return output{Done: true, Message: "run not found"}, nil
	}
	tp, err := h.store.GetTemplate(ctx, r.TemplateID, r.TemplateVersion)
	if err != nil {
		return output{Done: true, Message: "template not found"}, nil
	}
	stopFired, fired := false, []string{}
	if len(tp.StopAlarms) > 0 {
		out, err := h.cw.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: tp.StopAlarms})
		if err != nil {
			log.Warn().Err(err).Msg("describe stop alarms")
		} else {
			for _, a := range out.MetricAlarms {
				if string(a.StateValue) == "ALARM" {
					stopFired = true
					fired = append(fired, aws.ToString(a.AlarmName))
				}
			}
		}
	}

	duration := intFromAny(r.Params["durationSeconds"], 0)
	if duration <= 0 {
		duration = intFromAny(r.PolicySnapshot["maxRunDurationSeconds"], 0)
	}
	if duration <= 0 {
		duration = 300
	}
	start := r.RequestedAt
	if r.StartedAt != nil {
		start = *r.StartedAt
	}
	done := time.Now().UTC().After(start.Add(time.Duration(duration) * time.Second))
	_ = h.store.PutEvent(ctx, &model.ChaosRunEvent{RunID: r.RunID, Type: model.EventMonitor, Message: "Monitor tick", Details: map[string]any{"done": done, "stopAlarmFired": stopFired, "fired": fired}})
	return output{Done: done, StopAlarmFired: stopFired, StopAlarms: fired, Message: "ok"}, nil
}

func intFromAny(v any, def int) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	default:
		return def
	}
}
