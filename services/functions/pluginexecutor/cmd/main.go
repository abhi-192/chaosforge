package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"chaosforge/pkg/chaos/lambdautil"
	"chaosforge/pkg/chaos/model"
	"chaosforge/pkg/chaos/util"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/rs/zerolog/log"
)

type input struct {
	RunID  string `json:"runId"`
	Action string `json:"action"` // inject|rollback
}

type output struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type handler struct {
	store modelStore
	lmb   *awslambda.Client
}

type modelStore interface {
	GetRun(context.Context, string) (*model.ChaosRun, error)
	UpdateRun(context.Context, *model.ChaosRun) error
	GetTemplate(context.Context, string, int) (*model.ChaosTemplate, error)
	GetPlugin(context.Context, string, int) (*model.ChaosPlugin, error)
	PutEvent(context.Context, *model.ChaosRunEvent) error
}

func main() {
	util.InitLogger("pluginexecutor")
	cfg, err := lambdautil.LoadConfig(context.Background())
	if err != nil {
		panic(err)
	}
	st := lambdautil.NewStoreFromEnv(cfg)
	h := &handler{store: st, lmb: awslambda.NewFromConfig(cfg)}
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
	pluginID, pluginVer, err := model.ParsePluginRef(tp.PluginRef)
	if err != nil {
		return output{OK: false, Message: err.Error()}, nil
	}
	pl, err := h.store.GetPlugin(ctx, pluginID, pluginVer)
	if err != nil {
		return output{OK: false, Message: "plugin not found"}, nil
	}
	var targets []model.Target
	if m, ok := r.Targets.(map[string]any); ok {
		b, _ := json.Marshal(m["targets"])
		_ = json.Unmarshal(b, &targets)
	}
	action := model.PluginInject
	if in.Action == "rollback" {
		action = model.PluginRollback
	}
	preq := model.PluginRequest{
		RunID:    r.RunID,
		Action:   action,
		Env:      r.Env,
		Params:   r.Params,
		Accounts: r.Accounts,
		Regions:  r.Regions,
		Targets:  targets,
		Policy:   r.PolicySnapshot,
		State:    r.PluginState,
	}
	resp, err := h.invokePlugin(ctx, pl.LambdaArn, preq)
	if err != nil {
		_ = h.store.PutEvent(ctx, &model.ChaosRunEvent{RunID: r.RunID, Type: model.EventInject, Message: "Plugin action failed", Details: map[string]any{"action": in.Action, "error": err.Error()}})
		r.State = model.RunFailed
		r.Outcome = "PLUGIN_FAILED"
		_ = h.store.UpdateRun(ctx, r)
		return output{OK: false, Message: err.Error()}, nil
	}
	// Update state
	if resp.State != nil {
		r.PluginState = resp.State
	}
	now := time.Now().UTC()
	if in.Action == "rollback" {
		r.State = model.RunRollingBack
		r.Rollback = map[string]any{"at": now.Format(time.RFC3339)}
	} else {
		r.State = model.RunRunning
		if r.StartedAt == nil {
			r.StartedAt = &now
		}
	}
	_ = h.store.PutEvent(ctx, &model.ChaosRunEvent{RunID: r.RunID, Type: model.EventInject, Message: fmt.Sprintf("Plugin %s complete", in.Action), Details: map[string]any{"action": in.Action}})
	if err := h.store.UpdateRun(ctx, r); err != nil {
		log.Error().Err(err).Msg("update run")
	}
	return output{OK: true, Message: "ok"}, nil
}

func (h *handler) invokePlugin(ctx context.Context, arn string, req model.PluginRequest) (*model.PluginResponse, error) {
	b, _ := json.Marshal(req)
	out, err := h.lmb.Invoke(ctx, &awslambda.InvokeInput{FunctionName: aws.String(arn), Payload: b})
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
