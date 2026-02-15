package main

import (
	"context"
	"fmt"

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
	Mode  string `json:"mode"` // preflight|postcheck
}

type output struct {
	OK      bool     `json:"ok"`
	Bad     []string `json:"bad,omitempty"`
	Message string   `json:"message"`
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
	util.InitLogger("alarmchecker")
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
		return output{OK: false, Message: "run not found"}, nil
	}
	tp, err := h.store.GetTemplate(ctx, r.TemplateID, r.TemplateVersion)
	if err != nil {
		return output{OK: false, Message: "template not found"}, nil
	}
	alarms := tp.RequiredAlarms
	if in.Mode == "postcheck" {
		// Postcheck also validates required alarms are OK.
		alarms = tp.RequiredAlarms
	}
	if len(alarms) == 0 {
		return output{OK: true, Message: "no alarms configured"}, nil
	}
	out, err := h.cw.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: alarms})
	if err != nil {
		log.Error().Err(err).Msg("describe alarms")
		return output{OK: false, Message: "cloudwatch describe failed"}, nil
	}
	bad := []string{}
	found := map[string]bool{}
	for _, a := range out.MetricAlarms {
		name := aws.ToString(a.AlarmName)
		found[name] = true
		st := string(a.StateValue)
		if st != "OK" {
			bad = append(bad, fmt.Sprintf("%s=%s", name, st))
		}
	}
	for _, want := range alarms {
		if !found[want] {
			bad = append(bad, fmt.Sprintf("%s=NOT_FOUND", want))
		}
	}
	ok := len(bad) == 0
	_ = h.store.PutEvent(ctx, &model.ChaosRunEvent{RunID: r.RunID, Type: model.EventPrecheck, Message: "Alarm check", Details: map[string]any{"mode": in.Mode, "ok": ok, "bad": bad}})
	if ok {
		return output{OK: true, Message: "ok"}, nil
	}
	return output{OK: false, Bad: bad, Message: "alarms not OK"}, nil
}
