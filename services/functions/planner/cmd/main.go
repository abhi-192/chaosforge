package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"chaosforge/pkg/chaos/model"
	"chaosforge/pkg/chaos/policy"
	"chaosforge/pkg/chaos/store"
	"chaosforge/pkg/chaos/util"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/rs/zerolog/log"
)

type input struct {
	RunID string `json:"runId"`
}

type output struct {
	OK bool `json:"ok"`
}

type handler struct {
	st *store.DynamoStore
	lc *lambda.Client
}

func main() {
	util.InitLogger("planner")
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		panic(err)
	}
	db := dynamodb.NewFromConfig(cfg)
	st := store.NewDynamo(db)
	st.TemplatesTable = util.MustGetenv("CHAOS_TEMPLATES_TABLE")
	st.PluginsTable = util.MustGetenv("CHAOS_PLUGINS_TABLE")
	st.RunsTable = util.MustGetenv("CHAOS_RUNS_TABLE")
	st.EventsTable = util.MustGetenv("CHAOS_EVENTS_TABLE")
	st.PoliciesTable = util.MustGetenv("CHAOS_POLICIES_TABLE")

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

	// Call plugin plan
	preq := model.PluginRequest{
		RunID:   r.RunID,
		Action:  model.PluginPlan,
		Env:     r.Env,
		Params:  mergeParams(tpl.DefaultParams, r.Params),
		Accounts: r.Accounts,
		Regions:  r.Regions,
		Policy:  r.PolicySnapshot,
	}
	payload, _ := json.Marshal(preq)
	inv, err := h.lc.Invoke(ctx, &lambda.InvokeInput{
		FunctionName: &pl.LambdaArn,
		Payload:      payload,
	})
	if err != nil {
		return output{OK: false}, err
	}
	if inv.FunctionError != nil {
		log.Error().Str("fnerr", *inv.FunctionError).RawJSON("payload", inv.Payload).Msg("plugin plan failed")
		return output{OK: false}, fmt.Errorf("plugin plan error")
	}
	var pres model.PluginResponse
	if err := json.Unmarshal(inv.Payload, &pres); err != nil {
		return output{OK: false}, err
	}
	if !pres.OK {
		return output{OK: false}, fmt.Errorf("plugin plan returned not ok: %s", pres.Message)
	}

	// Enforce blast radius (best effort). Eligible count may be returned in state.
	eligible := len(pres.Targets)
	selected := len(pres.Targets)
	if br, ok := r.PolicySnapshot["blastRadiusLimits"].(map[string]any); ok {
		if err := policy.EnforceBlastRadius(pl.RequiredTargetRole, br, eligible, selected); err != nil {
			_ = h.st.PutEvent(ctx, &model.ChaosRunEvent{RunID: r.RunID, Type: model.EventPlan, Message: "Blast radius violation", Details: map[string]any{"error": err.Error()}})
			return output{OK: false}, err
		}
	}

	// Persist targets + plugin state
	r.Targets = pres.Targets
	if r.PluginState == nil {
		r.PluginState = map[string]any{}
	}
	r.PluginState[pluginID] = pres.State
	if err := h.st.UpdateRun(ctx, r); err != nil {
		return output{OK: false}, err
	}

	_ = h.st.PutEvent(ctx, &model.ChaosRunEvent{
		RunID:   r.RunID,
		Type:    model.EventPlan,
		Message: fmt.Sprintf("Planned %d targets for plugin %s", len(pres.Targets), pluginID),
		Details: map[string]any{"pluginId": pluginID, "targetCount": len(pres.Targets)},
	})
	for _, ev := range pres.Events {
		ev.RunID = r.RunID
		ev.Type = model.EventPlan
		_ = h.st.PutEvent(ctx, &ev)
	}

	// Mark run ready for inject stage. Keep PENDING_APPROVAL if approval required.
	if !r.ApprovalRequired {
		r.State = model.RunReady
	}
	if err := h.st.UpdateRun(ctx, r); err != nil {
		return output{OK: false}, err
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

var _ = time.Now
