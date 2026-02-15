package main

import (
	"context"
	"fmt"
	"time"

	"chaosforge/pkg/chaos/model"
	"chaosforge/pkg/chaos/store"
	"chaosforge/pkg/chaos/util"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/rs/zerolog/log"
)

type input struct {
	RunID string `json:"runId"`
	Mode  string `json:"mode"` // preflight|postcheck
}

type output struct {
	OK           bool     `json:"ok"`
	NonOKAlarms  []string `json:"nonOkAlarms"`
	CheckedAlarms []string `json:"checkedAlarms"`
}

type handler struct {
	st *store.DynamoStore
	cw *cloudwatch.Client
}

func main() {
	util.InitLogger("alarmchecker")
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		panic(err)
	}
	db := dynamodb.NewFromConfig(cfg)
	st := store.NewDynamo(db)
	st.TemplatesTable = util.MustGetenv("CHAOS_TEMPLATES_TABLE")
	st.RunsTable = util.MustGetenv("CHAOS_RUNS_TABLE")
	st.EventsTable = util.MustGetenv("CHAOS_EVENTS_TABLE")

	lambda.Start((&handler{st: st, cw: cloudwatch.NewFromConfig(cfg)}).Handle)
}

func (h *handler) Handle(ctx context.Context, in input) (output, error) {
	r, err := h.st.GetRun(ctx, in.RunID)
	if err != nil {
		return output{OK: false}, err
	}
	tpl, err := h.st.GetTemplate(ctx, r.TemplateID, r.TemplateVersion)
	if err != nil {
		return output{OK: false}, err
	}
	alarms := tpl.RequiredAlarms
	evType := model.EventPrecheck
	if in.Mode == "postcheck" {
		alarms = tpl.RequiredAlarms
		evType = model.EventPostcheck
	}
	if len(alarms) == 0 {
		return output{OK: true}, nil
	}

	window := getInt(r.PolicySnapshot["preflightWindowSeconds"], 0)
	deadline := time.Now().UTC().Add(time.Duration(window) * time.Second)
	if in.Mode != "preflight" {
		deadline = time.Now().UTC() // single check
	}

	for {
		ok, nonOk, err := h.check(ctx, alarms)
		if err != nil {
			return output{OK: false}, err
		}
		if !ok {
			_ = h.st.PutEvent(ctx, &model.ChaosRunEvent{RunID: r.RunID, Type: evType, Message: "Alarm check failed", Details: map[string]any{"nonOkAlarms": nonOk}})
			return output{OK: false, NonOKAlarms: nonOk, CheckedAlarms: alarms}, nil
		}
		if time.Now().UTC().After(deadline) {
			_ = h.st.PutEvent(ctx, &model.ChaosRunEvent{RunID: r.RunID, Type: evType, Message: fmt.Sprintf("Alarms OK (%s)", in.Mode), Details: map[string]any{"alarms": alarms}})
			return output{OK: true, CheckedAlarms: alarms}, nil
		}
		select {
		case <-ctx.Done():
			return output{OK: false}, ctx.Err()
		case <-time.After(30 * time.Second):
		}
	}
}

func (h *handler) check(ctx context.Context, alarms []string) (bool, []string, error) {
	out, err := h.cw.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: alarms})
	if err != nil {
		return false, nil, err
	}
	bad := make([]string, 0)
	for _, a := range out.MetricAlarms {
		if string(a.StateValue) != "OK" {
			bad = append(bad, *a.AlarmName)
		}
	}
	for _, a := range out.CompositeAlarms {
		if string(a.StateValue) != "OK" {
			bad = append(bad, *a.AlarmName)
		}
	}
	return len(bad) == 0, bad, nil
}

func getInt(v any, def int) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	default:
		return def
	}
}

var _ = log.Info
