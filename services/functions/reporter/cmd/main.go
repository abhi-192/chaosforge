package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"chaosforge/pkg/chaos/lambdautil"
	"chaosforge/pkg/chaos/model"
	"chaosforge/pkg/chaos/notify"
	"chaosforge/pkg/chaos/util"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

type input struct {
	RunID string `json:"runId"`
}

type output struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	S3Key   string `json:"s3Key,omitempty"`
}

type handler struct {
	store  modelStore
	upl    *manager.Uploader
	bucket string
	notify notify.Notifier
}

type modelStore interface {
	GetRun(context.Context, string) (*model.ChaosRun, error)
	UpdateRun(context.Context, *model.ChaosRun) error
	ListEvents(context.Context, string, int32) ([]model.ChaosRunEvent, error)
	PutEvent(context.Context, *model.ChaosRunEvent) error
}

func main() {
	util.InitLogger("reporter")
	cfg, err := lambdautil.LoadConfig(context.Background())
	if err != nil {
		panic(err)
	}
	st := lambdautil.NewStoreFromEnv(cfg)
	bucket := os.Getenv("CHAOS_EVIDENCE_BUCKET")
	upl := manager.NewUploader(s3.NewFromConfig(cfg))
	n, err := notify.NewFromEnv(cfg, ssm.NewFromConfig(cfg))
	if err != nil {
		panic(err)
	}
	h := &handler{store: st, upl: upl, bucket: bucket, notify: n}
	lambda.Start(h.handle)
}

func (h *handler) handle(ctx context.Context, in input) (output, error) {
	r, err := h.store.GetRun(ctx, in.RunID)
	if err != nil {
		return output{OK: false, Message: "run not found"}, nil
	}
	evs, _ := h.store.ListEvents(ctx, r.RunID, 200)
	rep := model.RunReport{Run: *r, Events: evs}
	b, _ := json.MarshalIndent(rep, "", "  ")
	key := fmt.Sprintf("%s/report.json", r.EvidencePrefix)
	if h.bucket != "" {
		_, _ = h.upl.Upload(ctx, &s3.PutObjectInput{
			Bucket:      aws.String(h.bucket),
			Key:         aws.String(key),
			Body:        bytes.NewReader(b),
			ContentType: aws.String("application/json"),
		})
	}
	now := time.Now().UTC()
	r.FinishedAt = &now
	if r.State != model.RunFailed && r.State != model.RunAborted {
		r.State = model.RunSucceeded
		r.Outcome = "SUCCEEDED"
	}
	_ = h.store.PutEvent(ctx, &model.ChaosRunEvent{RunID: r.RunID, Type: model.EventReport, Message: "Report generated", Details: map[string]any{"s3Key": key}})
	_ = h.store.UpdateRun(ctx, r)
	if h.notify != nil {
		_ = h.notify.Notify(ctx, notify.Message{Type: "RUN_FINISHED", Env: r.Env, RunID: r.RunID, Text: fmt.Sprintf("ChaosForge run finished: %s (%s)", r.RunID, r.State), Fields: map[string]string{"state": string(r.State), "outcome": r.Outcome}})
	}
	return output{OK: true, Message: "ok", S3Key: key}, nil
}
