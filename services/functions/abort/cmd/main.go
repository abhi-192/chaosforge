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
	RunID  string `json:"runId"`
	Reason string `json:"reason"`
}

type output struct {
	OK bool `json:"ok"`
}

type handler struct {
	st *store.DynamoStore
}

func main() {
	util.InitLogger("abort")
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		panic(err)
	}
	st := store.NewDynamo(dynamodb.NewFromConfig(cfg))
	st.RunsTable = util.MustGetenv("CHAOS_RUNS_TABLE")
	st.EventsTable = util.MustGetenv("CHAOS_EVENTS_TABLE")
	lambda.Start((&handler{st: st}).Handle)
}

func (h *handler) Handle(ctx context.Context, in input) (output, error) {
	r, err := h.st.GetRun(ctx, in.RunID)
	if err != nil {
		return output{OK: false}, err
	}
	now := time.Now().UTC()
	r.State = model.RunAborted
	r.Outcome = "ABORTED"
	r.EndedAt = &now
	_ = h.st.UpdateRun(ctx, r)
	_ = h.st.PutEvent(ctx, &model.ChaosRunEvent{RunID: r.RunID, Type: model.EventFinal, Message: "Run aborted", Details: map[string]any{"reason": in.Reason}})
	return output{OK: true}, nil
}
