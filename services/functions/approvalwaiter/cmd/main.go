package main

import (
	"context"
	"time"

	"chaosforge/pkg/chaos/model"
	"chaosforge/pkg/chaos/store"
	"chaosforge/pkg/chaos/util"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

type input struct {
	RunID     string `json:"runId"`
	TaskToken string `json:"taskToken"`
}

type output struct {
	OK bool `json:"ok"`
}

type handler struct {
	st *store.DynamoStore
}

func main() {
	util.InitLogger("approvalwaiter")
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		panic(err)
	}
	st := store.NewDynamo(dynamodb.NewFromConfig(cfg))
	st.ApprovalsTable = util.MustGetenv("CHAOS_APPROVALS_TABLE")
	st.RunsTable = util.MustGetenv("CHAOS_RUNS_TABLE")
	st.EventsTable = util.MustGetenv("CHAOS_EVENTS_TABLE")

	lambda.Start((&handler{st: st}).Handle)
}

func (h *handler) Handle(ctx context.Context, in input) (output, error) {
	now := time.Now().UTC()
	a := &model.ChaosApproval{
		ApprovalID:  in.RunID,
		RunID:       in.RunID,
		State:       model.ApprovalPending,
		TaskToken:   in.TaskToken,
		RequestedAt: now,
		TTL:         now.Add(24 * time.Hour).Unix(),
	}
	if err := h.st.PutApproval(ctx, a); err != nil {
		return output{OK: false}, err
	}
	r, err := h.st.GetRun(ctx, in.RunID)
	if err == nil {
		r.State = model.RunPendingApproval
		_ = h.st.UpdateRun(ctx, r)
	}
	_ = h.st.PutEvent(ctx, &model.ChaosRunEvent{RunID: in.RunID, Type: model.EventMonitor, Message: "Awaiting approval"})
	return output{OK: true}, nil
}
