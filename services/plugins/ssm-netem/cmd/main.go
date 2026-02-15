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
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

type plugin struct{ rt *pluginutil.Runtime }

func main() {
	util.InitLogger("plugin-ssm-netem")
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
		return p.run(ctx, req, true)
	case model.PluginRollback:
		return p.run(ctx, req, false)
	default:
		return model.PluginResponse{OK: false, Message: "unsupported action"}, nil
	}
}

type params struct {
	TargetTags     map[string]string `json:"targetTags"`
	MaxTargets     int              `json:"maxTargets"`
	LatencyMs      int              `json:"latencyMs"`
	LossPercent    float64          `json:"lossPercent"`
	Interface      string           `json:"interface"`
}

func parseParams(m map[string]any) params {
	b, _ := json.Marshal(m)
	var p params
	_ = json.Unmarshal(b, &p)
	if p.MaxTargets <= 0 {
		p.MaxTargets = 1
	}
	if p.Interface == "" {
		p.Interface = "eth0"
	}
	if p.TargetTags == nil {
		p.TargetTags = map[string]string{}
	}
	if p.LatencyMs <= 0 {
		p.LatencyMs = 200
	}
	return p
}

func (p *plugin) plan(ctx context.Context, req model.PluginRequest) (model.PluginResponse, error) {
	prm := parseParams(req.Params)
	candidates := make([]model.Target, 0)

	for _, acct := range req.Accounts {
		for _, region := range req.Regions {
			cfg, err := p.rt.AssumeTarget(ctx, acct, "ssm", req.Env, region)
			if err != nil {
				return model.PluginResponse{OK: false, Message: err.Error()}, nil
			}
			ec2c := ec2.NewFromConfig(cfg)
			ssmc := ssm.NewFromConfig(cfg)

			managed := map[string]bool{}
			mi, err := ssmc.DescribeInstanceInformation(ctx, &ssm.DescribeInstanceInformationInput{})
			if err != nil {
				return model.PluginResponse{OK: false, Message: err.Error()}, nil
			}
			for _, ii := range mi.InstanceInformationList {
				if ii.InstanceId != nil {
					managed[*ii.InstanceId] = true
				}
			}

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
					id := deref(inst.InstanceId)
					if !managed[id] {
						continue
					}
					candidates = append(candidates, model.Target{AccountID: acct, Region: region, Type: "SSM_MANAGED_INSTANCE", ID: id, Meta: map[string]string{}})
				}
			}
		}
	}

	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	if len(candidates) > prm.MaxTargets {
		candidates = candidates[:prm.MaxTargets]
	}

	state := map[string]any{"instances": candidates, "roleType": "ssm"}
	return model.PluginResponse{OK: true, Targets: candidates, State: state, Message: fmt.Sprintf("selected %d instances", len(candidates))}, nil
}

func (p *plugin) run(ctx context.Context, req model.PluginRequest, inject bool) (model.PluginResponse, error) {
	prm := parseParams(req.Params)
	for _, t := range req.Targets {
		cfg, err := p.rt.AssumeTarget(ctx, t.AccountID, "ssm", req.Env, t.Region)
		if err != nil {
			return model.PluginResponse{OK: false, Message: err.Error()}, nil
		}
		ssmc := ssm.NewFromConfig(cfg)
		cmd := "sudo tc qdisc del dev " + prm.Interface + " root || true"
		if inject {
			cmd = fmt.Sprintf("sudo tc qdisc add dev %s root netem delay %dms loss %.2f%%", prm.Interface, prm.LatencyMs, prm.LossPercent)
		}
		out, err := ssmc.SendCommand(ctx, &ssm.SendCommandInput{
			DocumentName: strPtr("AWS-RunShellScript"),
			InstanceIds:  []string{t.ID},
			Parameters: map[string][]string{
				"commands": {cmd},
			},
			CloudWatchOutputConfig: &ssmtypes.CloudWatchOutputConfig{CloudWatchOutputEnabled: boolPtr(true)},
		})
		if err != nil {
			return model.PluginResponse{OK: false, Message: err.Error()}, nil
		}
		_ = out
	}
	msg := "rollback"
	if inject {
		msg = "injected"
	}
	return model.PluginResponse{OK: true, Message: msg}, nil
}

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool { return &b }
func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
