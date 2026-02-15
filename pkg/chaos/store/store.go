package store

import (
	"context"

	"chaosforge/pkg/chaos/model"
)

type Store interface {
	GetRun(ctx context.Context, runID string) (*model.ChaosRun, error)
	PutRun(ctx context.Context, run *model.ChaosRun) error
	UpdateRun(ctx context.Context, run *model.ChaosRun) error

	GetTemplate(ctx context.Context, templateID string, version int) (*model.ChaosTemplate, error)
	PutTemplate(ctx context.Context, tpl *model.ChaosTemplate) error

	GetPlugin(ctx context.Context, pluginID string, version int) (*model.ChaosPlugin, error)
	PutPlugin(ctx context.Context, pl *model.ChaosPlugin) error

	GetLatestPolicy(ctx context.Context, env string) (*model.ChaosPolicy, error)
	PutPolicy(ctx context.Context, pol *model.ChaosPolicy) error

	PutEvent(ctx context.Context, ev *model.ChaosRunEvent) error
	ListEvents(ctx context.Context, runID string, limit int32) ([]model.ChaosRunEvent, error)

	GetApproval(ctx context.Context, approvalID string) (*model.ChaosApproval, error)
	PutApproval(ctx context.Context, ap *model.ChaosApproval) error
	UpdateApproval(ctx context.Context, ap *model.ChaosApproval) error

	PutAccount(ctx context.Context, acct *model.ChaosAccount) error
	GetAccount(ctx context.Context, accountID string) (*model.ChaosAccount, error)
	UpdateAccount(ctx context.Context, acct *model.ChaosAccount) error
}
