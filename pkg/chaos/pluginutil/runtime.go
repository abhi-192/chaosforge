package pluginutil

import (
	"context"
	"fmt"
	"strings"

	"chaosforge/pkg/chaos/awsx"
	"chaosforge/pkg/chaos/store"
	"chaosforge/pkg/chaos/util"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

type Runtime struct {
	Base  aws.Config
	STS   *sts.Client
	Store *store.DynamoStore

	OrgName          string
	DefaultRoleNames map[string]string // ec2|ssm|alb
}

func NewRuntime(ctx context.Context) (*Runtime, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	db := dynamodb.NewFromConfig(cfg)
	st := store.NewDynamo(db)
	st.AccountsTable = util.MustGetenv("CHAOS_ACCOUNTS_TABLE")

	org := util.MustGetenv("CHAOS_ORG_NAME")
	defaults := map[string]string{
		"ec2": util.Getenv("CHAOS_ROLE_DEFAULT_EC2", "ChaosTargetExecutionRole-EC2"),
		"ssm": util.Getenv("CHAOS_ROLE_DEFAULT_SSM", "ChaosTargetExecutionRole-SSM"),
		"alb": util.Getenv("CHAOS_ROLE_DEFAULT_ALB", "ChaosTargetExecutionRole-ALB"),
	}

	return &Runtime{Base: cfg, STS: sts.NewFromConfig(cfg), Store: st, OrgName: org, DefaultRoleNames: defaults}, nil
}

func (r *Runtime) ExternalID(env string) string {
	return fmt.Sprintf("%s/%s", r.OrgName, env)
}

func (r *Runtime) AssumeTarget(ctx context.Context, accountID string, roleType string, env string, region string) (aws.Config, error) {
	roleName := r.DefaultRoleNames[strings.ToLower(roleType)]
	if r.Store != nil {
		if acct, err := r.Store.GetAccount(ctx, accountID); err == nil {
			if acct.RoleNames != nil {
				if v, ok := acct.RoleNames[strings.ToLower(roleType)]; ok && v != "" {
					roleName = v
				}
			}
		}
	}
	roleArn := fmt.Sprintf("arn:aws:iam::%s:role/%s", accountID, roleName)
	return awsx.AssumeRoleConfig(ctx, r.Base, roleArn, r.ExternalID(env), region)
}
