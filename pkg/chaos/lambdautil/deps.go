package lambdautil

import (
	"context"

	"chaosforge/pkg/chaos/store"
	"chaosforge/pkg/chaos/util"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

// LoadConfig loads default AWS config.
func LoadConfig(ctx context.Context) (aws.Config, error) {
	return config.LoadDefaultConfig(ctx)
}

// NewStoreFromEnv creates a DynamoStore and wires table names from environment variables.
func NewStoreFromEnv(cfg aws.Config) *store.DynamoStore {
	db := dynamodb.NewFromConfig(cfg)
	st := store.NewDynamo(db)
	st.TemplatesTable = util.MustGetenv("CHAOS_TEMPLATES_TABLE")
	st.PluginsTable = util.MustGetenv("CHAOS_PLUGINS_TABLE")
	st.RunsTable = util.MustGetenv("CHAOS_RUNS_TABLE")
	st.EventsTable = util.MustGetenv("CHAOS_EVENTS_TABLE")
	st.ApprovalsTable = util.MustGetenv("CHAOS_APPROVALS_TABLE")
	st.AccountsTable = util.MustGetenv("CHAOS_ACCOUNTS_TABLE")
	st.PoliciesTable = util.MustGetenv("CHAOS_POLICIES_TABLE")
	return st
}
