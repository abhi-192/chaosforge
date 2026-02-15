package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"chaosforge/pkg/chaos/model"
	"chaosforge/pkg/chaos/store"
	"chaosforge/pkg/chaos/util"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/rs/zerolog/log"
)

type input struct {
	RunID  string `json:"runId"`
	Action string `json:"action"` // inject|rollback
}

type output struct {
	OK bool `json:"ok"`
}

type handler struct {
	st *store.DynamoStore
	lc *lambda.Client
}

func main() {
	util.InitLogger("pluginexecutor")
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		panic(err)
	}
	st := store.NewDynamo(dynamodb.NewFromConfig(cfg))
	st.TemplatesTable = util.MustGetenv("CHAOS_TEMPLATES_TABLE")
	st.PluginsTable = util.MustGetenv("CHAOS_PLUGINS_TABLE")
	st.RunsTable = util.MustGetenv("CHAOS_RUNS_TABLE")
	st.EventsTable = util.MustGetenv("CHAOS_EVENTS_TABLE")
	lambda.Start((&handler{st: st, lc: lambda.NewFromConfig(cfg)}).Handle)
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
	pluginID, pluginVer, err := model.ParsePluginRef(tpl.PluginRef)
	if err != nil {
		return output{OK: false}, err
	}
	pl, err := h.st.GetPlugin(ctx, pluginID, pluginVer)
	if err != nil {
		return output{OK: false}, err
	}

	action := model.PluginAction(in.Action)
	if action != model.PluginInject && action != model.PluginRollback {
		return output{OK: false}, errors.New("invalid action")
	}

	if action == model.PluginInject {
		r.State = model.RunRunning
		now := time.Now().UTC()
		r.StartedAt = &now
	} else {
		r.State = model.RunRollingBack
		r.Rollback = map[string]any{"startedAt": time.Now().UTC().Format(time.RFC3339)}
	}
	_ = h.st.UpdateRun(ctx, r)

	// best-effort decode planned targets
	targets := make([]model.Target, 0)
	b, _ := json.Marshal(r.Targets)
	_ = json.Unmarshal(b, &targets)

	preq := model.PluginRequest{
		RunID:   r.RunID,
		Action:  action,
		Env:     r.Env,
		Params:  mergeParams(tpl.DefaultParams, r.Params),
		Accounts: r.Accounts,
		Regions:  r.Regions,
		Targets:  targets,
		Policy:  r.PolicySnapshot,
	}
	payload, _ := json.Marshal(preq)
	inv, err := h.lc.Invoke(ctx, &lambda.InvokeInput{FunctionName: &pl.LambdaArn, Payload: payload})
	if err != nil {
		return output{OK: false}, err
	}
	if inv.FunctionError != nil {
		log.Error().Str("fnerr", *inv.FunctionError).RawJSON("payload", inv.Payload).Msg("plugin exec failed")
		return output{OK: false}, fmt.Errorf("plugin exec error")
	}
	var pres model.PluginResponse
	if err := json.Unmarshal(inv.Payload, &pres); err != nil {
		return output{OK: false}, err
	}
	if !pres.OK {
		return output{OK: false}, fmt.Errorf("plugin returned not ok: %s", pres.Message)
	}

	// Persist plugin state
	if r.PluginState == nil {
		r.PluginState = map[string]any{}
	}
	// Merge state with existing for plugin
	if cur, ok := r.PluginState[pluginID].(map[string]any); ok {
		for k, v := range pres.State {
			cur[k] = v
		}
		r.PluginState[pluginID] = cur
	} else {
		r.PluginState[pluginID] = pres.State
	}
	_ = h.st.UpdateRun(ctx, r)

	// Write events
	evType := model.EventInject
	if action == model.PluginRollback {
		evType = model.EventRollback
	}
	_ = h.st.PutEvent(ctx, &model.ChaosRunEvent{RunID: r.RunID, Type: evType, Message: fmt.Sprintf("Plugin %s %s", pluginID, action)})
	for _, ev := range pres.Events {
		ev.RunID = r.RunID
		if ev.Type == "" {
			ev.Type = evType
		}
		_ = h.st.PutEvent(ctx, &ev)
	}

	if action == model.PluginRollback {
		r.Rollback["status"] = "DONE"
		_ = h.st.UpdateRun(ctx, r)
	}
	return output{OK: true}, nil
}

func mergeParams(defaults map[string]any, overrides map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range defaults {
		out[k] = v
	}
	for k, v := range overrides {
		out[k] = v
	}
	return out
}
