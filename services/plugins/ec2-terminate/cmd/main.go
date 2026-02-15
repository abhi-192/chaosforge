package main

import (
	"context"
	"sort"

	"chaosforge/pkg/chaos/model"
	"chaosforge/pkg/chaos/pluginutil"
	"chaosforge/pkg/chaos/util"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/rs/zerolog/log"
)

func main() {
	util.InitLogger("plugin-ec2-terminate")
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
	// Best-effort: record ASG desired capacity for rollback.
	state := req.State
	if state == nil {
		state = map[string]any{}
	}
	asgDesired := map[string]int{}
	for _, t := range req.Targets {
		cfg, err := rt.AssumeTarget(ctx, t.AccountID, "ec2", req.Env, t.Region)
		if err != nil {
			return model.PluginResponse{OK: false, Message: err.Error()}, nil
		}
		asc := autoscaling.NewFromConfig(cfg)
		// Find ASG for instance (if any)
		ai, err := asc.DescribeAutoScalingInstances(ctx, &autoscaling.DescribeAutoScalingInstancesInput{InstanceIds: []string{t.ID}})
		if err == nil && len(ai.AutoScalingInstances) > 0 {
			asg := aws.ToString(ai.AutoScalingInstances[0].AutoScalingGroupName)
			if _, ok := asgDesired[asg]; !ok {
				gs, err := asc.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{AutoScalingGroupNames: []string{asg}})
				if err == nil && len(gs.AutoScalingGroups) > 0 {
					asgDesired[asg] = int(aws.ToInt32(gs.AutoScalingGroups[0].DesiredCapacity))
				}
			}
			t.Meta = map[string]any{"asg": asg}
		}
		ec2c := ec2.NewFromConfig(cfg)
		_, err = ec2c.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{t.ID}})
		if err != nil {
			return model.PluginResponse{OK: false, Message: err.Error()}, nil
		}
	}
	if len(asgDesired) > 0 {
		m := map[string]any{}
		for k, v := range asgDesired {
			m[k] = v
		}
		state["asgDesiredBefore"] = m
	}
	return model.PluginResponse{OK: true, Message: "terminated", State: state}, nil
}

func rollback(ctx context.Context, rt *pluginutil.Runtime, req model.PluginRequest) (model.PluginResponse, error) {
	// Best-effort rollback: restore ASG desired capacity.
	state := req.State
	if state == nil {
		return model.PluginResponse{OK: true, Message: "no state"}, nil
	}
	asgMap, _ := state["asgDesiredBefore"].(map[string]any)
	for _, t := range req.Targets {
		cfg, err := rt.AssumeTarget(ctx, t.AccountID, "ec2", req.Env, t.Region)
		if err != nil {
			continue
		}
		asc := autoscaling.NewFromConfig(cfg)
		for asgName, desiredAny := range asgMap {
			desired := int64(0)
			switch x := desiredAny.(type) {
			case int:
				desired = int64(x)
			case int64:
				desired = x
			case float64:
				desired = int64(x)
			}
			if desired <= 0 {
				continue
			}
			_, _ = asc.SetDesiredCapacity(ctx, &autoscaling.SetDesiredCapacityInput{AutoScalingGroupName: aws.String(asgName), DesiredCapacity: aws.Int32(int32(desired)), HonorCooldown: aws.Bool(false)})
		}
	}
	return model.PluginResponse{OK: true, Message: "rollback best-effort", State: state}, nil
}

func strp(s string) *string { return &s }
