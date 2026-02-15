package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"chaosforge/pkg/chaos/lambdautil"
	"chaosforge/pkg/chaos/model"
	"chaosforge/pkg/chaos/policy"
	"chaosforge/pkg/chaos/util"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/rs/zerolog/log"
)

type input struct {
	RunID string `json:"runId"`
}

type output struct {
	OK       bool           `json:"ok"`
	Eligible int            `json:"eligible"`
	Selected int            `json:"selected"`
	Targets  []model.Target `json:"targets"`
	Message  string         `json:"message"`
}

type handler struct {
	store modelStore
	lmb   *lambda.Client
}

// modelStore is minimal interface for planner.
type modelStore interface {
	GetRun(context.Context, string) (*model.ChaosRun, error)
	UpdateRun(context.Context, *model.ChaosRun) error
	GetTemplate(context.Context, string, int) (*model.ChaosTemplate, error)
	GetPlugin(context.Context, string, int) (*model.ChaosPlugin, error)
	GetLatestPolicy(context.Context, string) (*model.ChaosPolicy, error)
	PutEvent(context.Context, *model.ChaosRunEvent) error
}

func main() {
	util.InitLogger("planner")
	cfg, err := lambdautil.LoadConfig(context.Background())
	if err != nil {
		panic(err)
	}
	st := lambdautil.NewStoreFromEnv(cfg)
	h := &handler{store: st, lmb: lambda.NewFromConfig(cfg)}
	lambda.Start(h.handle)
}

func (h *handler) handle(ctx context.Context, in input) (output, error) {
	if in.RunID == "" {
		return output{OK: false, Message: "runId is required"}, nil
	}
	r, err := h.store.GetRun(ctx, in.RunID)
	if err != nil {
		return output{OK: false, Message: "run not found"}, nil
	}
	// Idempotency: if targets already planned, just return.
	if m, ok := r.Targets.(map[string]any); ok {
		if raw, ok := m["targets"]; ok {
			if ts, ok := raw.([]any); ok && len(ts) > 0 {
				// best-effort unmarshal
				b, _ := json.Marshal(ts)
				var outTargets []model.Target
				_ = json.Unmarshal(b, &outTargets)
				eligible := intFromAny(m["eligible"])
				return output{OK: true, Targets: outTargets, Eligible: eligible, Selected: len(outTargets), Message: "already planned"}, nil
			}
		}
	}

	tp, err := h.store.GetTemplate(ctx, r.TemplateID, r.TemplateVersion)
	if err != nil {
		return output{OK: false, Message: "template not found"}, nil
	}
	pluginID, pluginVer, err := model.ParsePluginRef(tp.PluginRef)
	if err != nil {
		return output{OK: false, Message: err.Error()}, nil
	}
	pl, err := h.store.GetPlugin(ctx, pluginID, pluginVer)
	if err != nil {
		return output{OK: false, Message: "plugin not found"}, nil
	}

	pol, err := h.store.GetLatestPolicy(ctx, r.Env)
	if err != nil {
		return output{OK: false, Message: "policy not found"}, nil
	}
	// Governance checks again (defense in depth)
	if err := policy.EvaluateFreezeWindows(time.Now().UTC(), pol.FreezeWindows); err != nil {
		_ = h.store.PutEvent(ctx, &model.ChaosRunEvent{RunID: r.RunID, Type: model.EventPlan, Message: "Blocked by freeze window", Details: map[string]any{"error": err.Error()}})
		r.State = model.RunAborted
		r.Outcome = "FREEZE_WINDOW"
		_ = h.store.UpdateRun(ctx, r)
		return output{OK: false, Message: err.Error()}, nil
	}
	if err := policy.EnforceAllowedPlugins(pluginID, pol.AllowedPlugins); err != nil {
		return output{OK: false, Message: err.Error()}, nil
	}
	merged, err := policy.EnsureTagScoping(r.Params, pol.RequiredTags, pol.DenyTags)
	if err != nil {
		return output{OK: false, Message: err.Error()}, nil
	}
	r.Params = merged
	// Snapshot policy for the run
	r.PolicySnapshot = utilMap(pol)
	if err := h.store.UpdateRun(ctx, r); err != nil {
		log.Warn().Err(err).Msg("update run before plan")
	}

	// Invoke plugin "plan"
	preq := model.PluginRequest{
		RunID:    r.RunID,
		Action:   model.PluginPlan,
		Env:      r.Env,
		Params:   r.Params,
		Accounts: r.Accounts,
		Regions:  r.Regions,
		Policy:   r.PolicySnapshot,
		State:    r.PluginState,
	}
	resp, err := h.invokePlugin(ctx, pl.LambdaArn, preq)
	if err != nil {
		_ = h.store.PutEvent(ctx, &model.ChaosRunEvent{RunID: r.RunID, Type: model.EventPlan, Message: "Plugin plan failed", Details: map[string]any{"error": err.Error()}})
		r.State = model.RunFailed
		r.Outcome = "PLAN_FAILED"
		_ = h.store.UpdateRun(ctx, r)
		return output{OK: false, Message: err.Error()}, nil
	}
	eligible := intFromAny(resp.State["eligible"])
	selected := len(resp.Targets)
	// Blast radius enforcement
	if err := policy.EnforceBlastRadius(pl.RequiredTargetRole, pol.BlastRadiusLimits, eligible, selected); err != nil {
		_ = h.store.PutEvent(ctx, &model.ChaosRunEvent{RunID: r.RunID, Type: model.EventPlan, Message: "Blast radius denied", Details: map[string]any{"error": err.Error(), "eligible": eligible, "selected": selected}})
		r.State = model.RunAborted
		r.Outcome = "BLAST_RADIUS_DENIED"
		_ = h.store.UpdateRun(ctx, r)
		return output{OK: false, Message: err.Error()}, nil
	}

	r.Targets = map[string]any{
		"targetType": pl.RequiredTargetRole,
		"eligible":   eligible,
		"selected":   selected,
		"targets":    resp.Targets,
	}
	// Persist plugin state for later rollback.
	if resp.State != nil {
		r.PluginState = resp.State
	}
	_ = h.store.PutEvent(ctx, &model.ChaosRunEvent{RunID: r.RunID, Type: model.EventPlan, Message: fmt.Sprintf("Planned %d/%d targets", selected, eligible), Details: map[string]any{"targetType": pl.RequiredTargetRole}})
	if err := h.store.UpdateRun(ctx, r); err != nil {
		log.Error().Err(err).Msg("update run")
		return output{OK: false, Message: "failed to persist plan"}, nil
	}
	return output{OK: true, Eligible: eligible, Selected: selected, Targets: resp.Targets, Message: "planned"}, nil
}

func (h *handler) invokePlugin(ctx context.Context, arn string, req model.PluginRequest) (*model.PluginResponse, error) {
	b, _ := json.Marshal(req)
	out, err := h.lmb.Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String(arn), Payload: b})
	if err != nil {
		return nil, err
	}
	if out.FunctionError != nil {
		return nil, fmt.Errorf("plugin error: %s", string(out.Payload))
	}
	var pr model.PluginResponse
	if err := json.Unmarshal(out.Payload, &pr); err != nil {
		return nil, err
	}
	if !pr.OK {
		return nil, fmt.Errorf("plugin returned not ok: %s", pr.Message)
	}
	return &pr, nil
}

func intFromAny(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	default:
		return 0
	}
}

func utilMap(pol *model.ChaosPolicy) map[string]any {
	b, _ := json.Marshal(pol)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}
