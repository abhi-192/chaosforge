package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"chaosforge/pkg/chaos/model"
	"chaosforge/pkg/chaos/pluginutil"
	"chaosforge/pkg/chaos/util"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

type plugin struct {
	rt *pluginutil.Runtime
}

func main() {
	util.InitLogger("plugin-ec2-stop")
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
		return p.inject(ctx, req, true)
	case model.PluginRollback:
		return p.inject(ctx, req, false)
	default:
		return model.PluginResponse{OK: false, Message: "unsupported action"}, nil
	}
}

type params struct {
	TargetTags map[string]string `json:"targetTags"`
	MaxTargets int              `json:"maxTargets"`
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
	candidates := make([]model.Target, 0)

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
					candidates = append(candidates, model.Target{AccountID: acct, Region: region, Type: "EC2_INSTANCE", ID: *inst.InstanceId, Meta: map[string]string{}})
				}
			}
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].AccountID == candidates[j].AccountID {
			if candidates[i].Region == candidates[j].Region {
				return candidates[i].ID < candidates[j].ID
			}
			return candidates[i].Region < candidates[j].Region
		}
		return candidates[i].AccountID < candidates[j].AccountID
	})

	if len(candidates) > prm.MaxTargets {
		candidates = candidates[:prm.MaxTargets]
	}

	state := map[string]any{"instances": candidates, "roleType": "ec2"}
	return model.PluginResponse{OK: true, Targets: candidates, State: state, Message: fmt.Sprintf("selected %d instances", len(candidates))}, nil
}

func (p *plugin) inject(ctx context.Context, req model.PluginRequest, stop bool) (model.PluginResponse, error) {
	for _, t := range req.Targets {
		cfg, err := p.rt.AssumeTarget(ctx, t.AccountID, "ec2", req.Env, t.Region)
		if err != nil {
			return model.PluginResponse{OK: false, Message: err.Error()}, nil
		}
		ec2c := ec2.NewFromConfig(cfg)
		if stop {
			_, err = ec2c.StopInstances(ctx, &ec2.StopInstancesInput{InstanceIds: []string{t.ID}})
		} else {
			_, err = ec2c.StartInstances(ctx, &ec2.StartInstancesInput{InstanceIds: []string{t.ID}})
		}
		if err != nil {
			return model.PluginResponse{OK: false, Message: err.Error()}, nil
		}
	}
	msg := "stopped"
	if !stop {
		msg = "started"
	}
	return model.PluginResponse{OK: true, Message: msg}, nil
}

func strPtr(s string) *string { return &s }
