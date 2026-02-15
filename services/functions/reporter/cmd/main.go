package main

import (
	"context"
	"time"

	"chaosforge/pkg/chaos/evidence"
	"chaosforge/pkg/chaos/model"
	"chaosforge/pkg/chaos/store"
	"chaosforge/pkg/chaos/util"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type input struct {
	RunID string `json:"runId"`
}

type output struct {
	OK bool `json:"ok"`
}

type handler struct {
	st *store.DynamoStore
	ev *evidence.Writer
}

func main() {
	util.InitLogger("reporter")
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		panic(err)
	}
	st := store.NewDynamo(dynamodb.NewFromConfig(cfg))
	st.RunsTable = util.MustGetenv("CHAOS_RUNS_TABLE")
	st.EventsTable = util.MustGetenv("CHAOS_EVENTS_TABLE")
	st.TemplatesTable = util.MustGetenv("CHAOS_TEMPLATES_TABLE")
	bucket := util.MustGetenv("CHAOS_EVIDENCE_BUCKET")
	kms := util.Getenv("CHAOS_EVIDENCE_KMS_KEY_ARN", "")
	ev := evidence.New(s3.NewFromConfig(cfg), bucket, kms)
	lambda.Start((&handler{st: st, ev: ev}).Handle)
}

func (h *handler) Handle(ctx context.Context, in input) (output, error) {
	r, err := h.st.GetRun(ctx, in.RunID)
	if err != nil {
		return output{OK: false}, err
	}
	tpl, _ := h.st.GetTemplate(ctx, r.TemplateID, r.TemplateVersion)
	evs, _ := h.st.ListEvents(ctx, r.RunID, 1000)

	// Final state
	final := model.RunSucceeded
	outcome := "SUCCESS"
	if r.Rollback != nil {
		if st, ok := r.Rollback["status"].(string); ok && st == "DONE" {
			final = model.RunFailed
			outcome = "ROLLED_BACK"
		}
	}
	now := time.Now().UTC()
	r.State = final
	r.Outcome = outcome
	r.EndedAt = &now
	_ = h.st.UpdateRun(ctx, r)

	report := map[string]any{
		"run":      r,
		"template": tpl,
		"events":   evs,
		"generatedAt": now.Format(time.RFC3339),
	}
	key := r.EvidencePrefix + "/report.json"
	_ = h.ev.PutJSON(ctx, key, report)
	_ = h.st.PutEvent(ctx, &model.ChaosRunEvent{RunID: r.RunID, Type: model.EventFinal, Message: "Run finalized", Details: map[string]any{"state": r.State, "outcome": r.Outcome, "reportKey": key}})
	return output{OK: true}, nil
}
