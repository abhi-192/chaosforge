package subcmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"chaosforge/pkg/chaos/config"
	"chaosforge/pkg/chaos/model"
	"chaosforge/pkg/chaos/store"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/spf13/cobra"
)

func NewSeedCmd() *cobra.Command {
	var (
		cfgPath string
		stack   string
		verify  bool
	)

	cmd := &cobra.Command{
		Use:   "seed",
		Short: "Seed DynamoDB with plugins, policies, accounts and templates from YAML config",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if cfgPath == "" {
				return errors.New("--config is required")
			}
			c, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			if stack == "" {
				stack = c.ControlPlane.StackName
			}
			if stack == "" {
				return errors.New("controlPlane.stackName is required (or pass --stack)")
			}

			awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(c.ControlPlane.Region))
			if err != nil {
				return err
			}

			cfn := cloudformation.NewFromConfig(awsCfg)
			outs, err := fetchStackOutputs(ctx, cfn, stack)
			if err != nil {
				return err
			}

			db := dynamodb.NewFromConfig(awsCfg)
			st := store.NewDynamo(db)
			applyTableNames(st, outs)

			lam := lambda.NewFromConfig(awsCfg)
			ssmClient := ssm.NewFromConfig(awsCfg)

			cmd.Printf("Seeding to stack %s in region %s\n", stack, c.ControlPlane.Region)
			cmd.Printf("  - Templates table: %s\n", st.TemplatesTable)
			cmd.Printf("  - Plugins table:   %s\n", st.PluginsTable)
			cmd.Printf("  - Policies table:  %s\n", st.PoliciesTable)
			cmd.Printf("  - Accounts table:  %s\n", st.AccountsTable)

			if err := seedAccounts(ctx, st, c); err != nil {
				return err
			}
			if err := seedPolicies(ctx, st, c); err != nil {
				return err
			}
			if err := seedPlugins(ctx, st, c, lam, awsCfg); err != nil {
				return err
			}
			if err := seedTemplates(ctx, st, c); err != nil {
				return err
			}
			if err := seedKillSwitchParams(ctx, ssmClient, c); err != nil {
				return err
			}

			if verify {
				if err := verifySeed(ctx, st, c); err != nil {
					return err
				}
				cmd.Println("Verification: OK")
			}
			cmd.Println("Seed complete.")
			return nil
		},
	}

	cmd.Flags().StringVarP(&cfgPath, "config", "c", "", "Path to chaosforge YAML config")
	cmd.Flags().StringVar(&stack, "stack", "", "CloudFormation stack name (defaults to controlPlane.stackName)")
	cmd.Flags().BoolVar(&verify, "verify", true, "Read back a small sample to verify seeding")

	return cmd
}

func fetchStackOutputs(ctx context.Context, cfn *cloudformation.Client, stackName string) (map[string]string, error) {
	out, err := cfn.DescribeStacks(ctx, &cloudformation.DescribeStacksInput{StackName: aws.String(stackName)})
	if err != nil {
		return nil, err
	}
	if len(out.Stacks) == 0 {
		return nil, fmt.Errorf("stack %q not found", stackName)
	}
	res := map[string]string{}
	for _, o := range out.Stacks[0].Outputs {
		if o.OutputKey != nil && o.OutputValue != nil {
			res[*o.OutputKey] = *o.OutputValue
		}
	}
	return res, nil
}

func applyTableNames(st *store.DynamoStore, outs map[string]string) {
	if v := outs["TemplatesTableName"]; v != "" {
		st.TemplatesTable = v
	}
	if v := outs["PluginsTableName"]; v != "" {
		st.PluginsTable = v
	}
	if v := outs["PoliciesTableName"]; v != "" {
		st.PoliciesTable = v
	}
	if v := outs["AccountsTableName"]; v != "" {
		st.AccountsTable = v
	}
}

func seedAccounts(ctx context.Context, st *store.DynamoStore, c *config.Config) error {
	for _, a := range c.TargetAccounts {
		acct := &model.ChaosAccount{
			AccountID:      a.AccountID,
			OUPath:         a.OUPath,
			Enabled:        a.Enabled,
			AllowedRegions: a.AllowedRegions,
			RoleNames:      a.RoleNames,
			Tags:           a.Tags,
		}
		if err := st.PutAccount(ctx, acct); err != nil {
			return fmt.Errorf("put account %s: %w", a.AccountID, err)
		}
	}
	return nil
}

func seedPolicies(ctx context.Context, st *store.DynamoStore, c *config.Config) error {
	now := time.Now().UTC()
	for env, p := range c.Policies {
		br := map[string]any{}
		for k, v := range p.BlastRadiusLimits {
			br[k] = map[string]any{"maxCount": v.MaxCount, "maxPercent": v.MaxPercent}
		}
		fw := make([]any, 0, len(p.FreezeWindows))
		for _, w := range p.FreezeWindows {
			fw = append(fw, map[string]any{
				"name":      w.Name,
				"tz":        w.TZ,
				"daysOfWeek": w.DaysOfWeek,
				"start":     w.Start,
				"end":       w.End,
			})
		}
		pol := &model.ChaosPolicy{
			Env:                   env,
			Version:               p.Version,
			BlastRadiusLimits:     br,
			AllowedPlugins:        p.AllowedPlugins,
			FreezeWindows:         fw,
			RequireApproval:       p.RequireApproval,
			RequiredTags:          p.RequiredTags,
			DenyTags:              p.DenyTags,
			MaxRunDurationSeconds: p.MaxRunDurationSeconds,
			PreflightWindowSeconds: p.PreflightWindowSeconds,
			CreatedAt:             now,
		}
		if err := st.PutPolicy(ctx, pol); err != nil {
			return fmt.Errorf("put policy %s: %w", env, err)
		}
	}
	return nil
}

func seedPlugins(ctx context.Context, st *store.DynamoStore, c *config.Config, lam *lambda.Client, awsCfg aws.Config) error {
	now := time.Now().UTC()
	for _, p := range c.Plugins {
		lambdaArn := ""
		if p.LambdaName != "" {
			gf, err := lam.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(p.LambdaName)})
			if err == nil && gf.Configuration != nil && gf.Configuration.FunctionArn != nil {
				lambdaArn = *gf.Configuration.FunctionArn
			}
			if lambdaArn == "" {
				// Fallback: compute ARN (works when function name is stable)
				lambdaArn = fmt.Sprintf("arn:aws:lambda:%s:%s:function:%s", awsCfg.Region, c.ControlPlane.AccountID, p.LambdaName)
			}
		}
		pl := &model.ChaosPlugin{
			PluginID:           p.PluginID,
			Version:            p.Version,
			LambdaArn:          lambdaArn,
			Description:        fmt.Sprintf("ChaosForge plugin %s", p.PluginID),
			Capabilities:       []string{"plan", "inject", "rollback"},
			RequiredTargetRole: strings.ToLower(p.RequiredTargetRoleType),
			ParamSchema:        map[string]any{},
			Status:             "ACTIVE",
			CreatedAt:          now,
		}
		if err := st.PutPlugin(ctx, pl); err != nil {
			return fmt.Errorf("put plugin %s@%d: %w", p.PluginID, p.Version, err)
		}
	}
	return nil
}

func seedTemplates(ctx context.Context, st *store.DynamoStore, c *config.Config) error {
	now := time.Now().UTC()
	for _, t := range c.Templates {
		pluginID, _, err := model.ParsePluginRef(t.PluginRef)
		if err != nil {
			return fmt.Errorf("template %s invalid pluginRef: %w", t.TemplateID, err)
		}
		status := model.TemplateDraft
		switch strings.ToUpper(strings.TrimSpace(t.Status)) {
		case "APPROVED":
			status = model.TemplateApproved
		case "DEPRECATED":
			status = model.TemplateDeprecated
		case "DRAFT", "":
			status = model.TemplateDraft
		default:
			return fmt.Errorf("template %s: invalid status %q", t.TemplateID, t.Status)
		}

		tpl := &model.ChaosTemplate{
			TemplateID:     t.TemplateID,
			Version:        t.Version,
			Name:           t.Name,
			Description:    t.Description,
			EnvScope:       t.EnvScope,
			PluginRef:      t.PluginRef,
			PluginID:       pluginID,
			ParamsSchema:   t.ParamsSchema,
			DefaultParams:  t.DefaultParams,
			RequiredAlarms: t.RequiredAlarms,
			StopAlarms:     t.StopAlarms,
			ReportConfig:   map[string]any{},
			Status:         status,
			CreatedBy:      "seed",
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		if err := st.PutTemplate(ctx, tpl); err != nil {
			return fmt.Errorf("put template %s@%d: %w", t.TemplateID, t.Version, err)
		}
	}
	return nil
}

func seedKillSwitchParams(ctx context.Context, ssmClient *ssm.Client, c *config.Config) error {
	// Create the kill-switch params for each env present in policies.
	for env := range c.Policies {
		name := fmt.Sprintf("/chaos/%s/kill-switch", env)
		_, err := ssmClient.PutParameter(ctx, &ssm.PutParameterInput{
			Name:      aws.String(name),
			Type:      ssmtypes.ParameterTypeString,
			Value:     aws.String("DISENGAGED"),
			Overwrite: aws.Bool(true),
		})
		if err != nil {
			return fmt.Errorf("put ssm parameter %s: %w", name, err)
		}
	}
	return nil
}

func verifySeed(ctx context.Context, st *store.DynamoStore, c *config.Config) error {
	// Minimal read-back verification: a single account, policy per env, template, plugin.
	if len(c.TargetAccounts) > 0 {
		_, err := st.GetAccount(ctx, c.TargetAccounts[0].AccountID)
		if err != nil {
			return fmt.Errorf("verify account: %w", err)
		}
	}
	for env := range c.Policies {
		_, err := st.GetLatestPolicy(ctx, env)
		if err != nil {
			return fmt.Errorf("verify policy %s: %w", env, err)
		}
		break
	}
	if len(c.Plugins) > 0 {
		_, err := st.GetPlugin(ctx, c.Plugins[0].PluginID, c.Plugins[0].Version)
		if err != nil {
			return fmt.Errorf("verify plugin: %w", err)
		}
	}
	if len(c.Templates) > 0 {
		_, err := st.GetTemplate(ctx, c.Templates[0].TemplateID, c.Templates[0].Version)
		if err != nil {
			return fmt.Errorf("verify template: %w", err)
		}
	}
	return nil
}
