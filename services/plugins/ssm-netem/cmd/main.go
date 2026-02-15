package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"chaosforge/pkg/chaos/model"
	"chaosforge/pkg/chaos/pluginutil"
	"chaosforge/pkg/chaos/util"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

func main() {
	util.InitLogger("plugin-ssm-netem")
	rt, err := pluginutil.NewRuntime(context.Background())
	if err != nil {
		panic(err)
	}
	lambda.Start(func(ctx context.Context, req model.PluginRequest) (model.PluginResponse, error) {
		return handle(ctx, rt, req)
	})
}

func handle(ctx context.Context, rt *pluginutil.Runtime, req model.PluginRequest) (model.PluginResponse, error) {
	switch req.Action {
	case model.PluginPlan:
		return plan(ctx, rt, req)
	case model.PluginInject:
		return inject(ctx, rt, req)
	case model.PluginRollback:
		return rollback(ctx, rt, req)
	default:
		return model.PluginResponse{OK: true, Message: "noop"}, nil
	}
}

func plan(ctx context.Context, rt *pluginutil.Runtime, req model.PluginRequest) (model.PluginResponse, error) {
	maxTargets := pluginutil.GetIntParam(req.Params, "maxTargets", 1)
	tags, err := pluginutil.GetStringMapParam(req.Params, "targetTags")
	if err != nil {
		return model.PluginResponse{OK: false, Message: err.Error()}, nil
	}
	eligible := []model.Target{}
	for _, acct := range req.Accounts {
		for _, region := range req.Regions {
			cfg, err := rt.AssumeTarget(ctx, acct, "ssm", req.Env, region)
			if err != nil {
				continue
			}
			ec2c := ec2.NewFromConfig(cfg)
			ssmc := ssm.NewFromConfig(cfg)
			managed := map[string]bool{}
			pmi := ssm.NewDescribeInstanceInformationPaginator(ssmc, &ssm.DescribeInstanceInformationInput{})
			for pmi.HasMorePages() {
				page, err := pmi.NextPage(ctx)
				if err != nil {
					break
				}
				for _, ii := range page.InstanceInformationList {
					if ii.PingStatus == ssmtypes.PingStatusOnline {
						managed[aws.ToString(ii.InstanceId)] = true
					}
				}
			}
			filters := []ec2types.Filter{{Name: strp("instance-state-name"), Values: []string{"running"}}}
			for k, v := range tags {
				if v == "" {
					filters = append(filters, ec2types.Filter{Name: strp("tag-key"), Values: []string{k}})
				} else {
					filters = append(filters, ec2types.Filter{Name: strp("tag:" + k), Values: []string{v}})
				}
			}
			p := ec2.NewDescribeInstancesPaginator(ec2c, &ec2.DescribeInstancesInput{Filters: filters})
			for p.HasMorePages() {
				page, err := p.NextPage(ctx)
				if err != nil {
					break
				}
				for _, res := range page.Reservations {
					for _, inst := range res.Instances {
						id := aws.ToString(inst.InstanceId)
						if managed[id] {
							eligible = append(eligible, model.Target{AccountID: acct, Region: region, Type: "ssm-instance", ID: id})
						}
					}
				}
			}
		}
	}
	sort.Slice(eligible, func(i, j int) bool {
		if eligible[i].AccountID != eligible[j].AccountID {
			return eligible[i].AccountID < eligible[j].AccountID
		}
		if eligible[i].Region != eligible[j].Region {
			return eligible[i].Region < eligible[j].Region
		}
		return eligible[i].ID < eligible[j].ID
	})
	sel := eligible
	if maxTargets > 0 && len(sel) > maxTargets {
		sel = sel[:maxTargets]
	}
	return model.PluginResponse{OK: true, Message: "planned", Targets: sel, State: map[string]any{"eligible": len(eligible), "selected": len(sel)}}, nil
}

func inject(ctx context.Context, rt *pluginutil.Runtime, req model.PluginRequest) (model.PluginResponse, error) {
	delay := pluginutil.GetIntParam(req.Params, "delayMs", 200)
	loss := pluginutil.GetIntParam(req.Params, "lossPercent", 0)
	iface, _ := req.Params["iface"].(string)
	if strings.TrimSpace(iface) == "" {
		iface = "eth0"
	}
	cmd := fmt.Sprintf("sudo tc qdisc replace dev %s root netem delay %dms", iface, delay)
	if loss > 0 {
		cmd = fmt.Sprintf("%s loss %d%%", cmd, loss)
	}
	for _, t := range req.Targets {
		cfg, err := rt.AssumeTarget(ctx, t.AccountID, "ssm", req.Env, t.Region)
		if err != nil {
			return model.PluginResponse{OK: false, Message: err.Error()}, nil
		}
		ssmc := ssm.NewFromConfig(cfg)
		_, err = ssmc.SendCommand(ctx, &ssm.SendCommandInput{
			InstanceIds:  []string{t.ID},
			DocumentName: aws.String("AWS-RunShellScript"),
			Parameters:   map[string][]string{"commands": {cmd}},
		})
		if err != nil {
			return model.PluginResponse{OK: false, Message: err.Error()}, nil
		}
	}
	return model.PluginResponse{OK: true, Message: "netem applied", State: req.State}, nil
}

func rollback(ctx context.Context, rt *pluginutil.Runtime, req model.PluginRequest) (model.PluginResponse, error) {
	iface, _ := req.Params["iface"].(string)
	if strings.TrimSpace(iface) == "" {
		iface = "eth0"
	}
	cmd := fmt.Sprintf("sudo tc qdisc del dev %s root netem || true", iface)
	for _, t := range req.Targets {
		cfg, err := rt.AssumeTarget(ctx, t.AccountID, "ssm", req.Env, t.Region)
		if err != nil {
			continue
		}
		ssmc := ssm.NewFromConfig(cfg)
		_, _ = ssmc.SendCommand(ctx, &ssm.SendCommandInput{
			InstanceIds:  []string{t.ID},
			DocumentName: aws.String("AWS-RunShellScript"),
			Parameters:   map[string][]string{"commands": {cmd}},
		})
	}
	return model.PluginResponse{OK: true, Message: "netem removed", State: req.State}, nil
}

func strp(s string) *string { return &s }
