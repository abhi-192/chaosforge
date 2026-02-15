package awsx

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// LoadConfig loads the default AWS config for the given region.
func LoadConfig(ctx context.Context, region string) (aws.Config, error) {
	return config.LoadDefaultConfig(ctx, config.WithRegion(region))
}

// AssumeRoleConfig returns an aws.Config that assumes a role in the target account.
func AssumeRoleConfig(ctx context.Context, base aws.Config, roleArn string, externalId string, region string) (aws.Config, error) {
	stsClient := sts.NewFromConfig(base)
	prov := stscreds.NewAssumeRoleProvider(stsClient, roleArn, func(o *stscreds.AssumeRoleOptions) {
		o.ExternalID = aws.String(externalId)
		o.RoleSessionName = fmt.Sprintf("chaosforge-%d", time.Now().UTC().Unix())
	})
	cfg := base
	cfg.Region = region
	cfg.Credentials = aws.NewCredentialsCache(prov)
	return cfg, nil
}
