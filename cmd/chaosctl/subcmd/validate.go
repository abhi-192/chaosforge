package subcmd

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"chaosforge/pkg/chaos/config"
	"chaosforge/pkg/chaos/model"
	"chaosforge/pkg/chaos/store"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/spf13/cobra"
)

type issueLevel string

const (
	levelErr  issueLevel = "ERROR"
	levelWarn issueLevel = "WARN"
	levelInfo issueLevel = "INFO"
)

type issue struct {
	Level issueLevel
	Code  string
	Msg   string
}

type issueSet struct {
	mu     sync.Mutex
	issues []issue
}

func (s *issueSet) add(lvl issueLevel, code, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.issues = append(s.issues, issue{Level: lvl, Code: code, Msg: msg})
}
func (s *issueSet) err(code, msg string)  { s.add(levelErr, code, msg) }
func (s *issueSet) warn(code, msg string) { s.add(levelWarn, code, msg) }
func (s *issueSet) info(code, msg string) { s.add(levelInfo, code, msg) }

func (s *issueSet) hasErrors() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, it := range s.issues {
		if it.Level == levelErr {
			return true
		}
	}
	return false
}

func (s *issueSet) print(cmd *cobra.Command) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sort.Slice(s.issues, func(i, j int) bool {
		if s.issues[i].Level != s.issues[j].Level {
			return s.issues[i].Level < s.issues[j].Level
		}
		return s.issues[i].Code < s.issues[j].Code
	})
	for _, it := range s.issues {
		cmd.Printf("[%s] %s: %s\n", it.Level, it.Code, it.Msg)
	}
}

func NewValidateCmd() *cobra.Command {
	var (
		cfgPath       string
		stack         string
		noAWS         bool
		strict        bool
		checkDDB      bool
		checkStackSet bool
		checkAssume   bool
		concurrency   int
	)

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate config, control-plane stack, StackSet rollout, and target-account access",
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
			if concurrency <= 0 {
				concurrency = 8
			}

			issues := &issueSet{}

			validateConfigSemantics(c, issues)

			if noAWS {
				issues.info("AWS.SKIPPED", "AWS checks skipped (--no-aws)")
				issues.print(cmd)
				if issues.hasErrors() {
					return fmt.Errorf("validation failed")
				}
				cmd.Println("Validation OK")
				return nil
			}

			awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(c.ControlPlane.Region))
			if err != nil {
				return err
			}

			outs := map[string]string{}
			if stack != "" {
				cfn := cloudformation.NewFromConfig(awsCfg)
				o, e := fetchStackOutputs(ctx, cfn, stack)
				if e != nil {
					issues.err("STACK.NOT_FOUND", e.Error())
				} else {
					outs = o
					validateStackOutputs(outs, issues)
				}
			} else {
				issues.warn("STACK.MISSING", "No stack name provided; skipping control-plane checks")
			}

			// DynamoDB + seeded item validation
			if checkDDB {
				if len(outs) == 0 {
					issues.warn("DDB.SKIPPED", "Skipping DynamoDB checks because stack outputs are unavailable")
				} else {
					db := dynamodb.NewFromConfig(awsCfg)
					st := store.NewDynamo(db)
					applyTableNames(st, outs)
					validateTablesExist(ctx, db, st, issues)
					validateSeededItems(ctx, st, c, issues)
				}
			}

			// Kill-switch parameter existence
			ssmClient := ssm.NewFromConfig(awsCfg)
			validateKillSwitchParams(ctx, ssmClient, c, issues)

			// Step Functions
			if arn := outs["OrchestratorArn"]; arn != "" {
				sfnc := sfn.NewFromConfig(awsCfg)
				validateStateMachine(ctx, sfnc, arn, issues)
			}

			// Plugin lambda existence
			lam := lambda.NewFromConfig(awsCfg)
			validatePluginFunctions(ctx, lam, c, issues)

			// StackSet rollout
			if checkStackSet {
				cfn := cloudformation.NewFromConfig(awsCfg)
				validateStackSet(ctx, cfn, c, issues)
			}

			// Cross-account assume role & smoke permissions
			if checkAssume {
				stsc := sts.NewFromConfig(awsCfg)
				validateAssumeAndPerms(ctx, awsCfg, stsc, c, concurrency, issues)
			}

			issues.print(cmd)
			if issues.hasErrors() || (strict && hasWarnings(issues)) {
				return fmt.Errorf("validation failed")
			}
			cmd.Println("Validation OK")
			return nil
		},
	}

	cmd.Flags().StringVarP(&cfgPath, "config", "c", "", "Path to chaosforge YAML config")
	cmd.Flags().StringVar(&stack, "stack", "", "Control-plane CloudFormation stack name (defaults to config controlPlane.stackName)")
	cmd.Flags().BoolVar(&noAWS, "no-aws", false, "Skip AWS checks (config-only validation)")
	cmd.Flags().BoolVar(&strict, "strict", false, "Fail on warnings")
	cmd.Flags().BoolVar(&checkDDB, "check-ddb", true, "Validate DynamoDB tables and seeded items")
	cmd.Flags().BoolVar(&checkStackSet, "check-stackset", true, "Validate StackSet and stack instances")
	cmd.Flags().BoolVar(&checkAssume, "check-assume", true, "Assume target roles and run minimal permission smoke tests")
	cmd.Flags().IntVar(&concurrency, "concurrency", 8, "Concurrency for cross-account validation")

	return cmd
}

func hasWarnings(s *issueSet) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, it := range s.issues {
		if it.Level == levelWarn {
			return true
		}
	}
	return false
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
	if v := outs["RunsTableName"]; v != "" {
		st.RunsTable = v
	}
	if v := outs["EventsTableName"]; v != "" {
		st.EventsTable = v
	}
	if v := outs["ApprovalsTableName"]; v != "" {
		st.ApprovalsTable = v
	}
}

func validateStackOutputs(outs map[string]string, issues *issueSet) {
	req := []string{
		"TemplatesTableName", "PluginsTableName", "PoliciesTableName", "AccountsTableName",
		"RunsTableName", "EventsTableName", "ApprovalsTableName", "EvidenceBucketName", "OrchestratorArn",
	}
	for _, k := range req {
		if outs[k] == "" {
			issues.err("STACK.OUTPUT_MISSING", fmt.Sprintf("required output %s is missing", k))
		}
	}
	if outs["ApiEndpoint"] == "" {
		issues.warn("STACK.API_MISSING", "ApiEndpoint output missing (CreateApi may be false)")
	}
}

func validateTablesExist(ctx context.Context, db *dynamodb.Client, st *store.DynamoStore, issues *issueSet) {
	tables := map[string]string{
		"Templates": st.TemplatesTable,
		"Plugins":   st.PluginsTable,
		"Policies":  st.PoliciesTable,
		"Accounts":  st.AccountsTable,
		"Runs":      st.RunsTable,
		"Events":    st.EventsTable,
		"Approvals": st.ApprovalsTable,
	}
	for name, tn := range tables {
		if tn == "" {
			issues.err("DDB.TABLE_MISSING", fmt.Sprintf("table name for %s is empty", name))
			continue
		}
		out, err := db.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(tn)})
		if err != nil {
			issues.err("DDB.DESCRIBE_FAILED", fmt.Sprintf("%s (%s): %v", name, tn, err))
			continue
		}
		if out.Table != nil && out.Table.TableStatus != "ACTIVE" {
			issues.warn("DDB.TABLE_NOT_ACTIVE", fmt.Sprintf("%s (%s) status=%s", name, tn, out.Table.TableStatus))
		}
	}
}

func validateSeededItems(ctx context.Context, st *store.DynamoStore, c *config.Config, issues *issueSet) {
	for _, p := range c.Plugins {
		if _, err := st.GetPlugin(ctx, p.PluginID, p.Version); err != nil {
			issues.err("SEED.PLUGIN_MISSING", fmt.Sprintf("plugin %s@%d not found in DynamoDB: %v", p.PluginID, p.Version, err))
		}
	}
	for _, t := range c.Templates {
		if _, err := st.GetTemplate(ctx, t.TemplateID, t.Version); err != nil {
			issues.err("SEED.TEMPLATE_MISSING", fmt.Sprintf("template %s@%d not found in DynamoDB: %v", t.TemplateID, t.Version, err))
		}
	}
	for env := range c.Policies {
		if _, err := st.GetLatestPolicy(ctx, env); err != nil {
			issues.err("SEED.POLICY_MISSING", fmt.Sprintf("policy %s not found in DynamoDB: %v", env, err))
		}
	}
	for _, a := range c.TargetAccounts {
		if _, err := st.GetAccount(ctx, a.AccountID); err != nil {
			issues.err("SEED.ACCOUNT_MISSING", fmt.Sprintf("account %s not found in DynamoDB: %v", a.AccountID, err))
		}
	}
}

func validateKillSwitchParams(ctx context.Context, ssmc *ssm.Client, c *config.Config, issues *issueSet) {
	envs := make([]string, 0, len(c.Policies))
	for env := range c.Policies {
		envs = append(envs, env)
	}
	sort.Strings(envs)
	for _, env := range envs {
		name := fmt.Sprintf("/chaos/%s/kill-switch", env)
		out, err := ssmc.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(name)})
		if err != nil {
			issues.warn("SSM.KILLSWITCH_MISSING", fmt.Sprintf("%s: %v", name, err))
			continue
		}
		if out.Parameter != nil && out.Parameter.Value != nil {
			v := strings.ToUpper(strings.TrimSpace(*out.Parameter.Value))
			if v != "ENGAGED" && v != "DISENGAGED" {
				issues.warn("SSM.KILLSWITCH_VALUE", fmt.Sprintf("%s has unexpected value %q", name, v))
			}
		}
	}
}

func validateStateMachine(ctx context.Context, sfnc *sfn.Client, arn string, issues *issueSet) {
	out, err := sfnc.DescribeStateMachine(ctx, &sfn.DescribeStateMachineInput{StateMachineArn: aws.String(arn)})
	if err != nil {
		issues.err("SFN.DESCRIBE_FAILED", fmt.Sprintf("%s: %v", arn, err))
		return
	}
	if out.Status != "ACTIVE" {
		issues.warn("SFN.NOT_ACTIVE", fmt.Sprintf("state machine status=%s", out.Status))
	}
	// Best-effort: ensure a few critical state names exist in the definition.
	def := aws.ToString(out.Definition)
	need := []string{"LoadRun", "PlanTargets", "ApprovalGate", "WaitForApproval", "Inject", "MonitorLoop", "Rollback", "Finalize"}
	for _, n := range need {
		if !strings.Contains(def, fmt.Sprintf("\"%s\"", n)) {
			issues.warn("SFN.STATE_MISSING", fmt.Sprintf("definition missing state %s (best-effort check)", n))
		}
	}
}

func validatePluginFunctions(ctx context.Context, lam *lambda.Client, c *config.Config, issues *issueSet) {
	for _, p := range c.Plugins {
		if strings.TrimSpace(p.LambdaName) == "" {
			issues.warn("PLUGIN.LAMBDA_NAME", fmt.Sprintf("plugin %s@%d has empty lambdaName", p.PluginID, p.Version))
			continue
		}
		_, err := lam.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(p.LambdaName)})
		if err != nil {
			issues.warn("LAMBDA.NOT_FOUND", fmt.Sprintf("%s (%s): %v", p.PluginID, p.LambdaName, err))
		}
	}
}

func validateStackSet(ctx context.Context, cfn *cloudformation.Client, c *config.Config, issues *issueSet) {
	name := c.StackSets.StackSetName
	if name == "" {
		issues.warn("STACKSET.SKIPPED", "stackSets.stackSetName empty; skipping StackSet checks")
		return
	}
	out, err := cfn.DescribeStackSet(ctx, &cloudformation.DescribeStackSetInput{StackSetName: aws.String(name)})
	if err != nil {
		issues.warn("STACKSET.DESCRIBE_FAILED", fmt.Sprintf("%s: %v", name, err))
		return
	}
	ss := out.StackSet
	if ss == nil {
		issues.warn("STACKSET.MISSING", fmt.Sprintf("%s: describe returned nil", name))
		return
	}
	if ss.PermissionModel != types.PermissionModelsServiceManaged {
		issues.err("STACKSET.PERMISSION_MODEL", fmt.Sprintf("expected SERVICE_MANAGED, got %s", ss.PermissionModel))
	}
	if ss.AutoDeployment == nil || !aws.ToBool(ss.AutoDeployment.Enabled) {
		issues.warn("STACKSET.AUTODEPLOY", "autoDeployment.enabled is false")
	}
	if ss.ManagedExecution == nil || !aws.ToBool(ss.ManagedExecution.Active) {
		issues.warn("STACKSET.MANAGEDEXEC", "managedExecution.active is false")
	}

	// Check instances for enabled accounts.
	instOut, err := cfn.ListStackInstances(ctx, &cloudformation.ListStackInstancesInput{StackSetName: aws.String(name)})
	if err != nil {
		issues.warn("STACKSET.LIST_INSTANCES_FAILED", fmt.Sprintf("%s: %v", name, err))
		return
	}
	byAcctRegion := map[string]map[string]types.StackInstanceSummary{}
	for _, si := range instOut.Summaries {
		acct := aws.ToString(si.Account)
		reg := aws.ToString(si.Region)
		m := byAcctRegion[acct]
		if m == nil {
			m = map[string]types.StackInstanceSummary{}
			byAcctRegion[acct] = m
		}
		m[reg] = si
	}

	for _, a := range c.TargetAccounts {
		if !a.Enabled {
			continue
		}
		wantRegions := effectiveRegions(a, c.StackSets.Regions)
		m := byAcctRegion[a.AccountID]
		if m == nil {
			issues.warn("STACKSET.INSTANCE_MISSING", fmt.Sprintf("no stack instances found for account %s", a.AccountID))
			continue
		}
		for _, r := range wantRegions {
			si, ok := m[r]
			if !ok {
				issues.warn("STACKSET.INSTANCE_MISSING", fmt.Sprintf("account %s missing instance in region %s", a.AccountID, r))
				continue
			}
			if si.Status != types.StackInstanceStatusCurrent {
				issues.warn("STACKSET.INSTANCE_NOT_CURRENT", fmt.Sprintf("account %s region %s status=%s", a.AccountID, r, si.Status))
			}
			if si.DetailedStatus == types.StackInstanceDetailedStatusInoperable {
				issues.err("STACKSET.INSTANCE_INOPERABLE", fmt.Sprintf("account %s region %s detailedStatus=%s", a.AccountID, r, si.DetailedStatus))
			}
		}
	}
}

func effectiveRegions(a config.TargetAccount, defaultRegions []string) []string {
	if len(a.AllowedRegions) == 0 {
		return uniqueStrings(defaultRegions)
	}
	set := map[string]bool{}
	for _, r := range a.AllowedRegions {
		set[r] = true
	}
	res := []string{}
	for _, r := range defaultRegions {
		if set[r] {
			res = append(res, r)
		}
	}
	if len(res) == 0 {
		// fall back to allowedRegions if they didn't intersect
		res = uniqueStrings(a.AllowedRegions)
	}
	return uniqueStrings(res)
}

func uniqueStrings(in []string) []string {
	m := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || m[s] {
			continue
		}
		m[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func validateAssumeAndPerms(ctx context.Context, base aws.Config, stsc *sts.Client, c *config.Config, concurrency int, issues *issueSet) {
	// Determine which role types are needed based on plugins.
	needRoleTypes := map[string]bool{}
	for _, p := range c.Plugins {
		needRoleTypes[strings.ToLower(strings.TrimSpace(p.RequiredTargetRoleType))] = true
	}

	envs := make([]string, 0, len(c.Policies))
	for env := range c.Policies {
		envs = append(envs, env)
	}
	sort.Strings(envs)

	type task struct {
		env      string
		account  config.TargetAccount
		roleType string
	}
	tasks := make(chan task)

	var wg sync.WaitGroup
	worker := func() {
		defer wg.Done()
		for t := range tasks {
			externalID := renderExternalID(c.ExternalIDFormat, c.OrgName, t.env)
			roleName := strings.TrimSpace(t.account.RoleNames[t.roleType])
			if roleName == "" {
				issues.err("ASSUME.ROLE_NAME_MISSING", fmt.Sprintf("account %s missing roleNames[%s]", t.account.AccountID, t.roleType))
				continue
			}
			roleArn := iamRoleArn(t.account.AccountID, roleName)

			prov := stscreds.NewAssumeRoleProvider(stsc, roleArn, func(o *stscreds.AssumeRoleOptions) {
				o.ExternalID = aws.String(externalID)
				o.RoleSessionName = fmt.Sprintf("chaosctl-%s-%s", t.roleType, time.Now().UTC().Format("20060102T150405"))
			})

			assumedCfg := base
			assumedCfg.Credentials = aws.NewCredentialsCache(prov)

			// Smoke test in a chosen region.
			region := pickRegion(t.account, c.StackSets.Regions, c.ControlPlane.Region)
			assumedCfg.Region = region

			switch t.roleType {
			case "ec2":
				cli := ec2.NewFromConfig(assumedCfg)
				_, err := cli.DescribeInstances(ctx, &ec2.DescribeInstancesInput{MaxResults: aws.Int32(5)})
				if err != nil {
					issues.err("ASSUME.EC2_FAILED", fmt.Sprintf("env=%s account=%s region=%s role=%s: %v", t.env, t.account.AccountID, region, roleArn, err))
				} else {
					issues.info("ASSUME.EC2_OK", fmt.Sprintf("env=%s account=%s region=%s", t.env, t.account.AccountID, region))
				}
			case "ssm":
				cli := ssm.NewFromConfig(assumedCfg)
				_, err := cli.DescribeInstanceInformation(ctx, &ssm.DescribeInstanceInformationInput{MaxResults: aws.Int32(5)})
				if err != nil {
					issues.err("ASSUME.SSM_FAILED", fmt.Sprintf("env=%s account=%s region=%s role=%s: %v", t.env, t.account.AccountID, region, roleArn, err))
				} else {
					issues.info("ASSUME.SSM_OK", fmt.Sprintf("env=%s account=%s region=%s", t.env, t.account.AccountID, region))
				}
			case "alb":
				cli := elasticloadbalancingv2.NewFromConfig(assumedCfg)
				_, err := cli.DescribeTargetGroups(ctx, &elasticloadbalancingv2.DescribeTargetGroupsInput{PageSize: aws.Int32(5)})
				if err != nil {
					issues.err("ASSUME.ALB_FAILED", fmt.Sprintf("env=%s account=%s region=%s role=%s: %v", t.env, t.account.AccountID, region, roleArn, err))
				} else {
					issues.info("ASSUME.ALB_OK", fmt.Sprintf("env=%s account=%s region=%s", t.env, t.account.AccountID, region))
				}
			default:
				issues.warn("ASSUME.UNKNOWN_ROLETYPE", fmt.Sprintf("unknown roleType %q", t.roleType))
			}
		}
	}

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go worker()
	}

	for _, env := range envs {
		for _, acct := range c.TargetAccounts {
			if !acct.Enabled {
				continue
			}
			for rt := range needRoleTypes {
				if rt == "" {
					continue
				}
				tasks <- task{env: env, account: acct, roleType: rt}
			}
		}
	}
	close(tasks)
	wg.Wait()
}

func pickRegion(a config.TargetAccount, defaultRegions []string, fallback string) string {
	regs := effectiveRegions(a, defaultRegions)
	if len(regs) > 0 {
		return regs[0]
	}
	if fallback != "" {
		return fallback
	}
	return "us-east-1"
}

func iamRoleArn(accountID, roleName string) string {
	roleName = strings.TrimPrefix(roleName, "/")
	return fmt.Sprintf("arn:aws:iam::%s:role/%s", accountID, roleName)
}

func renderExternalID(tpl, org, env string) string {
	s := tpl
	s = strings.ReplaceAll(s, "${ORG}", org)
	s = strings.ReplaceAll(s, "${ENV}", env)
	return s
}

func validateConfigSemantics(c *config.Config, issues *issueSet) {
	acctRe := regexp.MustCompile(`^\d{12}$`)
	seenAcct := map[string]bool{}
	for _, a := range c.TargetAccounts {
		if !acctRe.MatchString(a.AccountID) {
			issues.err("CFG.ACCOUNT_ID", fmt.Sprintf("invalid accountId %q", a.AccountID))
		}
		if seenAcct[a.AccountID] {
			issues.err("CFG.ACCOUNT_DUP", fmt.Sprintf("duplicate accountId %s", a.AccountID))
		}
		seenAcct[a.AccountID] = true
		if a.Enabled && len(a.AllowedRegions) == 0 {
			issues.warn("CFG.ALLOWED_REGIONS", fmt.Sprintf("account %s has no allowedRegions; will fall back to stackSets.regions", a.AccountID))
		}
	}

	// plugins map
	pluginKey := map[string]config.PluginDef{}
	for _, p := range c.Plugins {
		k := fmt.Sprintf("%s@%d", p.PluginID, p.Version)
		if _, ok := pluginKey[k]; ok {
			issues.err("CFG.PLUGIN_DUP", fmt.Sprintf("duplicate plugin %s", k))
		}
		rt := strings.ToLower(strings.TrimSpace(p.RequiredTargetRoleType))
		switch rt {
		case "ec2", "ssm", "alb":
		default:
			issues.err("CFG.PLUGIN_ROLETYPE", fmt.Sprintf("plugin %s has invalid requiredTargetRoleType %q", k, p.RequiredTargetRoleType))
		}
		if p.TimeoutSeconds <= 0 {
			issues.warn("CFG.PLUGIN_TIMEOUT", fmt.Sprintf("plugin %s timeoutSeconds is %d", k, p.TimeoutSeconds))
		}
		pluginKey[k] = p
	}

	// templates refer to plugin
	validEnvScope := map[string]bool{"dev": true, "prod": true, "any": true, "": true}
	validStatus := map[string]bool{"DRAFT": true, "APPROVED": true, "DEPRECATED": true, "": true}
	for _, t := range c.Templates {
		if _, err := model.ParsePluginRef(t.PluginRef); err != nil {
			issues.err("CFG.TEMPLATE_PLUGINREF", fmt.Sprintf("template %s invalid pluginRef: %v", t.TemplateID, err))
		} else {
			if _, ok := pluginKey[strings.TrimSpace(t.PluginRef)]; !ok {
				issues.warn("CFG.TEMPLATE_PLUGIN_MISSING", fmt.Sprintf("template %s references pluginRef %s not present in plugins list", t.TemplateID, t.PluginRef))
			}
		}
		if !validEnvScope[strings.ToLower(strings.TrimSpace(t.EnvScope))] {
			issues.err("CFG.ENVSCOPE", fmt.Sprintf("template %s invalid envScope %q", t.TemplateID, t.EnvScope))
		}
		if !validStatus[strings.ToUpper(strings.TrimSpace(t.Status))] {
			issues.err("CFG.TEMPLATE_STATUS", fmt.Sprintf("template %s invalid status %q", t.TemplateID, t.Status))
		}
	}

	// policies
	envs := make([]string, 0, len(c.Policies))
	for env := range c.Policies {
		envs = append(envs, env)
	}
	sort.Strings(envs)
	for _, env := range envs {
		p := c.Policies[env]
		if p.Version <= 0 {
			issues.warn("CFG.POLICY_VERSION", fmt.Sprintf("policy %s version is %d", env, p.Version))
		}
		if p.MaxRunDurationSeconds <= 0 {
			issues.err("CFG.POLICY_DURATION", fmt.Sprintf("policy %s maxRunDurationSeconds must be >0", env))
		}
		if p.PreflightWindowSeconds < 0 {
			issues.err("CFG.PREFLIGHT", fmt.Sprintf("policy %s preflightWindowSeconds must be >=0", env))
		}
		for k, v := range p.BlastRadiusLimits {
			if v.MaxCount < 0 {
				issues.err("CFG.BLAST_COUNT", fmt.Sprintf("policy %s blastRadiusLimits[%s].maxCount < 0", env, k))
			}
			if v.MaxPercent < 0 || v.MaxPercent > 100 {
				issues.err("CFG.BLAST_PERCENT", fmt.Sprintf("policy %s blastRadiusLimits[%s].maxPercent must be 0..100", env, k))
			}
		}
		// freeze windows best-effort parse
		for _, w := range p.FreezeWindows {
			if strings.TrimSpace(w.TZ) == "" {
				issues.warn("CFG.FREEZE_TZ", fmt.Sprintf("policy %s freezeWindow %q missing tz", env, w.Name))
				continue
			}
			if _, err := time.LoadLocation(w.TZ); err != nil {
				issues.err("CFG.FREEZE_TZ", fmt.Sprintf("policy %s freezeWindow %q invalid tz %q: %v", env, w.Name, w.TZ, err))
			}
			if strings.TrimSpace(w.Start) == "" || strings.TrimSpace(w.End) == "" {
				issues.err("CFG.FREEZE_TIME", fmt.Sprintf("policy %s freezeWindow %q must have start and end", env, w.Name))
			}
		}
		// Allowed plugins should exist in plugins list
		for _, ap := range p.AllowedPlugins {
			if ap == "" {
				continue
			}
			if _, ok := pluginKey[ap]; !ok {
				issues.warn("CFG.ALLOWED_PLUGIN_MISSING", fmt.Sprintf("policy %s allows %s not present in plugins list", env, ap))
			}
		}
	}

	// externalId format sanity
	if !strings.Contains(c.ExternalIDFormat, "${ORG}") || !strings.Contains(c.ExternalIDFormat, "${ENV}") {
		issues.warn("CFG.EXTERNAL_ID", fmt.Sprintf("externalIdFormat %q does not include both ${ORG} and ${ENV}", c.ExternalIDFormat))
	}
}
