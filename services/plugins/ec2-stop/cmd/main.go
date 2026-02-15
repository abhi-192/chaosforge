package main

import (
	"context"
	"sort"
	"time"

	"chaosforge/pkg/chaos/model"
	"chaosforge/pkg/chaos/pluginutil"
	"chaosforge/pkg/chaos/util"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/rs/zerolog/log"
)

func main() {
	util.InitLogger("plugin-ec2-stop")
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
			cfg, err := rt.AssumeTarget(ctx, acct, "ec2", req.Env, region)
			if err != nil {
				log.Warn().Err(err).Str("account", acct).Str("region", region).Msg("assume")
				continue
			}
			ec2c := ec2.NewFromConfig(cfg)
			filters := []types.Filter{{Name: strp("instance-state-name"), Values: []string{"running"}}}
			for k, v := range tags {
				if v == "" {
					filters = append(filters, types.Filter{Name: strp("tag-key"), Values: []string{k}})
				} else {
					filters = append(filters, types.Filter{Name: strp("tag:" + k), Values: []string{v}})
				}
			}
			p := ec2.NewDescribeInstancesPaginator(ec2c, &ec2.DescribeInstancesInput{Filters: filters})
			for p.HasMorePages() {
				page, err := p.NextPage(ctx)
				if err != nil {
					log.Warn().Err(err).Msg("describe instances")
					break
				}
				for _, res := range page.Reservations {
					for _, inst := range res.Instances {
						id := aws.ToString(inst.InstanceId)
						eligible = append(eligible, model.Target{AccountID: acct, Region: region, Type: "ec2-instance", ID: id})
					}
				}
			}
		}
	}
	// Deterministic pick
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
	for _, t := range req.Targets {
		cfg, err := rt.AssumeTarget(ctx, t.AccountID, "ec2", req.Env, t.Region)
		if err != nil {
			return model.PluginResponse{OK: false, Message: err.Error()}, nil
		}
		ec2c := ec2.NewFromConfig(cfg)
		_, err = ec2c.StopInstances(ctx, &ec2.StopInstancesInput{InstanceIds: []string{t.ID}})
		if err != nil {
			return model.PluginResponse{OK: false, Message: err.Error()}, nil
		}
	}
	// allow a short settle; plugins should generally be async.
	_ = time.Now()
	return model.PluginResponse{OK: true, Message: "stopped", State: req.State}, nil
}

func rollback(ctx context.Context, rt *pluginutil.Runtime, req model.PluginRequest) (model.PluginResponse, error) {
	for _, t := range req.Targets {
		cfg, err := rt.AssumeTarget(ctx, t.AccountID, "ec2", req.Env, t.Region)
		if err != nil {
			return model.PluginResponse{OK: false, Message: err.Error()}, nil
		}
		ec2c := ec2.NewFromConfig(cfg)
		_, err = ec2c.StartInstances(ctx, &ec2.StartInstancesInput{InstanceIds: []string{t.ID}})
		if err != nil {
			return model.PluginResponse{OK: false, Message: err.Error()}, nil
		}
	}
	return model.PluginResponse{OK: true, Message: "started", State: req.State}, nil
}

func strp(s string) *string { return &s }
