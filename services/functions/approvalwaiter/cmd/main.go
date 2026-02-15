package main

import (
	"context"
	"time"

	"chaosforge/pkg/chaos/lambdautil"
	"chaosforge/pkg/chaos/model"
	"chaosforge/pkg/chaos/notify"
	"chaosforge/pkg/chaos/util"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

type input struct {
	RunID     string `json:"runId"`
	TaskToken string `json:"taskToken"`
}

type output struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type handler struct {
	store  modelStore
	notify notify.Notifier
}

type modelStore interface {
	GetRun(context.Context, string) (*model.ChaosRun, error)
	PutApproval(context.Context, *model.ChaosApproval) error
	PutEvent(context.Context, *model.ChaosRunEvent) error
}

func main() {
	util.InitLogger("approvalwaiter")
	cfg, err := lambdautil.LoadConfig(context.Background())
	if err != nil {
		panic(err)
	}
	st := lambdautil.NewStoreFromEnv(cfg)
	n, err := notify.NewFromEnv(cfg, ssm.NewFromConfig(cfg))
	if err != nil {
		panic(err)
	}
	h := &handler{store: st, notify: n}
	lambda.Start(h.handle)
}

func (h *handler) handle(ctx context.Context, in input) (output, error) {
	r, err := h.store.GetRun(ctx, in.RunID)
	if err != nil {
		return output{OK: false, Message: "run not found"}, nil
	}
	// TTL default: 24h
	ttl := time.Now().UTC().Add(24 * time.Hour).Unix()
	ap := &model.ChaosApproval{
		ApprovalID:  r.RunID,
		RunID:       r.RunID,
		State:       model.ApprovalPending,
		TaskToken:   in.TaskToken,
		RequestedAt: time.Now().UTC(),
		TTL:         ttl,
	}
	if err := h.store.PutApproval(ctx, ap); err != nil {
		return output{OK: false, Message: "failed to store approval"}, nil
	}
	_ = h.store.PutEvent(ctx, &model.ChaosRunEvent{RunID: r.RunID, Type: model.EventPlan, Message: "Approval requested"})
	if h.notify != nil {
		_ = h.notify.Notify(ctx, notify.Message{Type: "APPROVAL_REQUESTED", Env: r.Env, RunID: r.RunID, Text: "ChaosForge approval requested", Fields: map[string]string{"runId": r.RunID, "env": r.Env}})
	}
	return output{OK: true, Message: "stored"}, nil
}
