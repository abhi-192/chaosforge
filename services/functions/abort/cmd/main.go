package main

import (
	"context"
	"time"

	"chaosforge/pkg/chaos/lambdautil"
	"chaosforge/pkg/chaos/model"
	"chaosforge/pkg/chaos/util"

	"github.com/aws/aws-lambda-go/lambda"
)

type input struct {
	RunID  string `json:"runId"`
	Reason string `json:"reason"`
}

type output struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type handler struct{ store modelStore }

type modelStore interface {
	GetRun(context.Context, string) (*model.ChaosRun, error)
	UpdateRun(context.Context, *model.ChaosRun) error
	PutEvent(context.Context, *model.ChaosRunEvent) error
}

func main() {
	util.InitLogger("abort")
	cfg, err := lambdautil.LoadConfig(context.Background())
	if err != nil {
		panic(err)
	}
	st := lambdautil.NewStoreFromEnv(cfg)
	h := &handler{store: st}
	lambda.Start(h.handle)
}

func (h *handler) handle(ctx context.Context, in input) (output, error) {
	r, err := h.store.GetRun(ctx, in.RunID)
	if err != nil {
		return output{OK: false, Message: "run not found"}, nil
	}
	r.State = model.RunAborted
	if in.Reason == "" {
		in.Reason = "ABORTED"
	}
	r.Outcome = in.Reason
	now := time.Now().UTC()
	r.FinishedAt = &now
	_ = h.store.PutEvent(ctx, &model.ChaosRunEvent{RunID: r.RunID, Type: model.EventAbort, Message: "Run aborted", Details: map[string]any{"reason": in.Reason}})
	_ = h.store.UpdateRun(ctx, r)
	return output{OK: true, Message: "aborted"}, nil
}
