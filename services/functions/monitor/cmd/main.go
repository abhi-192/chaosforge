package main

import (
	"context"
	"encoding/json"
	"time"

	"chaosforge/pkg/chaos/model"
	"chaosforge/pkg/chaos/store"
	"chaosforge/pkg/chaos/util"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

type input struct {
	RunID string `json:"runId"`
}

type output struct {
	StopAlarmFired bool `json:"stopAlarmFired"`
	Done           bool `json:"done"`
}

type handler struct {
	st *store.DynamoStore
	cw *cloudwatch.Client
}

func main() {
	util.InitLogger("monitor")
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		panic(err)
	}
	st := store.NewDynamo(dynamodb.NewFromConfig(cfg))
	st.TemplatesTable = util.MustGetenv("CHAOS_TEMPLATES_TABLE")
	st.RunsTable = util.MustGetenv("CHAOS_RUNS_TABLE")
	st.EventsTable = util.MustGetenv("CHAOS_EVENTS_TABLE")
	lambda.Start((&handler{st: st, cw: cloudwatch.NewFromConfig(cfg)}).Handle)
}

func (h *handler) Handle(ctx context.Context, in input) (output, error) {
	r, err := h.st.GetRun(ctx, in.RunID)
	if err != nil {
		return output{}, err
	}
	tpl, err := h.st.GetTemplate(ctx, r.TemplateID, r.TemplateVersion)
	if err != nil {
		return output{}, err
	}
	stop := tpl.StopAlarms
	fired := false
	if len(stop) > 0 {
		ok, nonOk, err := checkAlarms(ctx, h.cw, stop)
		if err != nil {
			return output{}, err
		}
		if !ok {
			fired = true
			_ = h.st.PutEvent(ctx, &model.ChaosRunEvent{RunID: r.RunID, Type: model.EventAlarm, Message: "Stop alarm fired", Details: map[string]any{"alarms": nonOk}})
		}
	}

	done := false
	if r.StartedAt != nil {
		elapsed := time.Since(*r.StartedAt)
		// Prefer template param durationSeconds, fallback to policy maxRunDurationSeconds
		dur := getIntParam(r.Params, "durationSeconds", 0)
		if dur == 0 {
			dur = getInt(r.PolicySnapshot["maxRunDurationSeconds"], 0)
		}
		if dur > 0 && elapsed >= time.Duration(dur)*time.Second {
			done = true
		}
	}

	// emit periodic monitor event
	_ = h.st.PutEvent(ctx, &model.ChaosRunEvent{RunID: r.RunID, Type: model.EventMonitor, Message: "Monitor tick", Details: map[string]any{"stopAlarmFired": fired, "done": done}})
	return output{StopAlarmFired: fired, Done: done}, nil
}

func checkAlarms(ctx context.Context, cw *cloudwatch.Client, alarms []string) (bool, []string, error) {
	out, err := cw.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: alarms})
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

func getIntParam(m map[string]any, key string, def int) int {
	if m == nil {
		return def
	}
	v, ok := m[key]
	if !ok {
		return def
	}
	return getInt(v, def)
}

var _ = json.Valid
