package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"chaosforge/pkg/chaos/model"
	"chaosforge/pkg/chaos/pluginutil"
	"chaosforge/pkg/chaos/util"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

type plugin struct{ rt *pluginutil.Runtime }

func main() {
	util.InitLogger("plugin-ec2-terminate")
	rt, err := pluginutil.NewRuntime(context.Background())
	if err != nil {
		panic(err)
	}
	lambda.Start((&plugin{rt: rt}).Handle)
}

func (p *plugin) Handle(ctx context.Context, req model.PluginRequest) (model.PluginResponse, error) {
	switch req.Action {
	case model.PluginPlan:
		return p.plan(ctx, req)
	case model.PluginInject:
		return p.terminate(ctx, req)
	case model.PluginRollback:
		return p.rollback(ctx, req)
	default:
		return model.PluginResponse{OK: false, Message: "unsupported action"}, nil
	}
}

type params struct {
	TargetTags  map[string]string `json:"targetTags"`
	MaxTargets  int              `json:"maxTargets"`
	AsgRequired bool             `json:"asgRequired"`
}

func parseParams(m map[string]any) params {
	b, _ := json.Marshal(m)
	var p params
	_ = json.Unmarshal(b, &p)
	if p.MaxTargets <= 0 {
		p.MaxTargets = 1
	}
	if p.TargetTags == nil {
		p.TargetTags = map[string]string{}
	}
	return p
}

func (p *plugin) plan(ctx context.Context, req model.PluginRequest) (model.PluginResponse, error) {
	prm := parseParams(req.Params)
	// enforce ASG managed in prod unless explicitly overridden
	if strings.ToLower(req.Env) == "prod" {
		prm.AsgRequired = true
	}
	candidates := make([]model.Target, 0)
	asgByInstance := map[string]string{}

	for _, acct := range req.Accounts {
		for _, region := range req.Regions {
			cfg, err := p.rt.AssumeTarget(ctx, acct, "ec2", req.Env, region)
			if err != nil {
				return model.PluginResponse{OK: false, Message: err.Error()}, nil
			}
			ec2c := ec2.NewFromConfig(cfg)
			filters := []ec2types.Filter{{Name: strPtr("instance-state-name"), Values: []string{"running"}}}
			for k, v := range prm.TargetTags {
				filters = append(filters, ec2types.Filter{Name: strPtr(fmt.Sprintf("tag:%s", k)), Values: []string{v}})
			}
			out, err := ec2c.DescribeInstances(ctx, &ec2.DescribeInstancesInput{Filters: filters})
			if err != nil {
				return model.PluginResponse{OK: false, Message: err.Error()}, nil
			}
			for _, res := range out.Reservations {
				for _, inst := range res.Instances {
					asg := ""
					for _, tag := range inst.Tags {
						if tag.Key != nil && *tag.Key == "aws:autoscaling:groupName" {
							asg = deref(tag.Value)
						}
					}
					if prm.AsgRequired && asg == "" {
						continue
					}
					id := deref(inst.InstanceId)
					candidates = append(candidates, model.Target{AccountID: acct, Region: region, Type: "EC2_INSTANCE", ID: id, Meta: map[string]string{"asg": asg}})
					if asg != "" {
						asgByInstance[id] = asg
					}
				}
			}
		}
	}

	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	if len(candidates) > prm.MaxTargets {
		candidates = candidates[:prm.MaxTargets]
	}
	state := map[string]any{"instances": candidates, "asgByInstance": asgByInstance}
	return model.PluginResponse{OK: true, Targets: candidates, State: state, Message: fmt.Sprintf("selected %d instances", len(candidates))}, nil
}

func (p *plugin) terminate(ctx context.Context, req model.PluginRequest) (model.PluginResponse, error) {
	for _, t := range req.Targets {
		cfg, err := p.rt.AssumeTarget(ctx, t.AccountID, "ec2", req.Env, t.Region)
		if err != nil {
			return model.PluginResponse{OK: false, Message: err.Error()}, nil
		}
		ec2c := ec2.NewFromConfig(cfg)
		_, err = ec2c.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{t.ID}})
		if err != nil {
			return model.PluginResponse{OK: false, Message: err.Error()}, nil
		}
	}
	return model.PluginResponse{OK: true, Message: "terminated"}, nil
}

func (p *plugin) rollback(ctx context.Context, req model.PluginRequest) (model.PluginResponse, error) {
	// Rollback strategy: bump ASG desired capacity by count per ASG
	inc := map[string]int{}
	for _, t := range req.Targets {
		asg := ""
		if t.Meta != nil {
			asg = t.Meta["asg"]
		}
		if asg == "" {
			continue
		}
		inc[fmt.Sprintf("%s|%s|%s", t.AccountID, t.Region, asg)]++
	}
	for key, n := range inc {
		parts := strings.Split(key, "|")
		if len(parts) != 3 {
			continue
		}
		acct, region, asgName := parts[0], parts[1], parts[2]
		cfg, err := p.rt.AssumeTarget(ctx, acct, "ec2", req.Env, region)
		if err != nil {
			return model.PluginResponse{OK: false, Message: err.Error()}, nil
		}
		asg := autoscaling.NewFromConfig(cfg)
		groups, err := asg.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{AutoScalingGroupNames: []string{asgName}})
		if err != nil {
			return model.PluginResponse{OK: false, Message: err.Error()}, nil
		}
		if len(groups.AutoScalingGroups) == 0 {
			return model.PluginResponse{OK: false, Message: "asg not found"}, nil
		}
		desired := int(derefInt32(groups.AutoScalingGroups[0].DesiredCapacity))
		newDesired := desired + n
		_, err = asg.SetDesiredCapacity(ctx, &autoscaling.SetDesiredCapacityInput{AutoScalingGroupName: &asgName, DesiredCapacity: int32Ptr(int32(newDesired)), HonorCooldown: boolPtr(false)})
		if err != nil {
			return model.PluginResponse{OK: false, Message: err.Error()}, nil
		}
	}
	if len(inc) == 0 {
		return model.PluginResponse{OK: false, Message: "no asg-managed targets to rollback"}, errors.New("rollback unsupported")
	}
	return model.PluginResponse{OK: true, Message: "rollback initiated"}, nil
}

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool { return &b }
func int32Ptr(v int32) *int32 { return &v }

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefInt32(p *int32) int32 {
	if p == nil {
		return 0
	}
	return *p
}
