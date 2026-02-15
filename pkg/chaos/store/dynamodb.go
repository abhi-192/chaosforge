package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"chaosforge/pkg/chaos/model"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type DynamoStore struct {
	db *dynamodb.Client

	TemplatesTable string
	PluginsTable   string
	RunsTable      string
	EventsTable    string
	ApprovalsTable string
	AccountsTable  string
	PoliciesTable  string
}

func NewDynamo(db *dynamodb.Client) *DynamoStore {
	return &DynamoStore{
		db:             db,
		TemplatesTable: model.TableTemplates,
		PluginsTable:   model.TablePlugins,
		RunsTable:      model.TableRuns,
		EventsTable:    model.TableEvents,
		ApprovalsTable: model.TableApprovals,
		AccountsTable:  model.TableAccounts,
		PoliciesTable:  model.TablePolicies,
	}
}

func (s *DynamoStore) GetRun(ctx context.Context, runID string) (*model.ChaosRun, error) {
	out, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.RunsTable),
		Key: map[string]types.AttributeValue{
			"runId": &types.AttributeValueMemberS{Value: runID},
		},
	})
	if err != nil {
		return nil, err
	}
	if out.Item == nil {
		return nil, errors.New("run not found")
	}
	var r model.ChaosRun
	if err := attributevalue.UnmarshalMap(out.Item, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *DynamoStore) PutRun(ctx context.Context, run *model.ChaosRun) error {
	item, err := attributevalue.MarshalMap(run)
	if err != nil {
		return err
	}
	_, err = s.db.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(s.RunsTable), Item: item})
	return err
}

func (s *DynamoStore) UpdateRun(ctx context.Context, run *model.ChaosRun) error {
	// Simplest: overwrite (idempotent), because run is authoritative.
	return s.PutRun(ctx, run)
}

func (s *DynamoStore) GetTemplate(ctx context.Context, templateID string, version int) (*model.ChaosTemplate, error) {
	out, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.TemplatesTable),
		Key: map[string]types.AttributeValue{
			"templateId": &types.AttributeValueMemberS{Value: templateID},
			"version":    &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", version)},
		},
	})
	if err != nil {
		return nil, err
	}
	if out.Item == nil {
		return nil, errors.New("template not found")
	}
	var t model.ChaosTemplate
	if err := attributevalue.UnmarshalMap(out.Item, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *DynamoStore) PutTemplate(ctx context.Context, tpl *model.ChaosTemplate) error {
	item, err := attributevalue.MarshalMap(tpl)
	if err != nil {
		return err
	}
	_, err = s.db.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(s.TemplatesTable), Item: item})
	return err
}

func (s *DynamoStore) GetPlugin(ctx context.Context, pluginID string, version int) (*model.ChaosPlugin, error) {
	out, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.PluginsTable),
		Key: map[string]types.AttributeValue{
			"pluginId": &types.AttributeValueMemberS{Value: pluginID},
			"version":  &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", version)},
		},
	})
	if err != nil {
		return nil, err
	}
	if out.Item == nil {
		return nil, errors.New("plugin not found")
	}
	var p model.ChaosPlugin
	if err := attributevalue.UnmarshalMap(out.Item, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *DynamoStore) PutPlugin(ctx context.Context, pl *model.ChaosPlugin) error {
	item, err := attributevalue.MarshalMap(pl)
	if err != nil {
		return err
	}
	_, err = s.db.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(s.PluginsTable), Item: item})
	return err
}

func (s *DynamoStore) GetLatestPolicy(ctx context.Context, env string) (*model.ChaosPolicy, error) {
	// Query by PK env, sort key version descending is not available unless we use ScanIndexForward=false with Query.
	out, err := s.db.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(s.PoliciesTable),
		KeyConditionExpression: aws.String("#e = :env"),
		ExpressionAttributeNames: map[string]string{
			"#e": "env",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":env": &types.AttributeValueMemberS{Value: env},
		},
		ScanIndexForward: aws.Bool(false),
		Limit:            aws.Int32(1),
	})
	if err != nil {
		return nil, err
	}
	if len(out.Items) == 0 {
		return nil, errors.New("policy not found")
	}
	var p model.ChaosPolicy
	if err := attributevalue.UnmarshalMap(out.Items[0], &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *DynamoStore) PutPolicy(ctx context.Context, pol *model.ChaosPolicy) error {
	item, err := attributevalue.MarshalMap(pol)
	if err != nil {
		return err
	}
	_, err = s.db.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(s.PoliciesTable), Item: item})
	return err
}

func (s *DynamoStore) PutEvent(ctx context.Context, ev *model.ChaosRunEvent) error {
	if ev.SK == "" {
		ev.SK = fmt.Sprintf("ts#%d#%d", time.Now().UTC().UnixMilli(), time.Now().UTC().UnixNano()%1_000_000)
	}
	item, err := attributevalue.MarshalMap(ev)
	if err != nil {
		return err
	}
	_, err = s.db.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(s.EventsTable), Item: item})
	return err
}

func (s *DynamoStore) ListEvents(ctx context.Context, runID string, limit int32) ([]model.ChaosRunEvent, error) {
	out, err := s.db.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(s.EventsTable),
		KeyConditionExpression: aws.String("runId = :rid"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":rid": &types.AttributeValueMemberS{Value: runID},
		},
		Limit:            aws.Int32(limit),
		ScanIndexForward: aws.Bool(true),
	})
	if err != nil {
		return nil, err
	}
	var res []model.ChaosRunEvent
	if err := attributevalue.UnmarshalListOfMaps(out.Items, &res); err != nil {
		return nil, err
	}
	return res, nil
}

func (s *DynamoStore) GetApproval(ctx context.Context, approvalID string) (*model.ChaosApproval, error) {
	out, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.ApprovalsTable),
		Key: map[string]types.AttributeValue{
			"approvalId": &types.AttributeValueMemberS{Value: approvalID},
		},
	})
	if err != nil {
		return nil, err
	}
	if out.Item == nil {
		return nil, errors.New("approval not found")
	}
	var a model.ChaosApproval
	if err := attributevalue.UnmarshalMap(out.Item, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *DynamoStore) PutApproval(ctx context.Context, ap *model.ChaosApproval) error {
	item, err := attributevalue.MarshalMap(ap)
	if err != nil {
		return err
	}
	_, err = s.db.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(s.ApprovalsTable), Item: item})
	return err
}

func (s *DynamoStore) UpdateApproval(ctx context.Context, ap *model.ChaosApproval) error {
	return s.PutApproval(ctx, ap)
}

func (s *DynamoStore) PutAccount(ctx context.Context, acct *model.ChaosAccount) error {
	item, err := attributevalue.MarshalMap(acct)
	if err != nil {
		return err
	}
	_, err = s.db.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(s.AccountsTable), Item: item})
	return err
}

func (s *DynamoStore) UpdateAccount(ctx context.Context, acct *model.ChaosAccount) error {
	return s.PutAccount(ctx, acct)
}

func (s *DynamoStore) GetAccount(ctx context.Context, accountID string) (*model.ChaosAccount, error) {
	out, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.AccountsTable),
		Key: map[string]types.AttributeValue{
			"accountId": &types.AttributeValueMemberS{Value: accountID},
		},
	})
	if err != nil {
		return nil, err
	}
	if out.Item == nil {
		return nil, errors.New("account not found")
	}
	var a model.ChaosAccount
	if err := attributevalue.UnmarshalMap(out.Item, &a); err != nil {
		return nil, err
	}
	return &a, nil
}
