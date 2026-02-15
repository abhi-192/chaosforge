package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sort"

	"chaosforge/pkg/chaos/model"
	"chaosforge/pkg/chaos/pluginutil"
	"chaosforge/pkg/chaos/util"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
)

type plugin struct{ rt *pluginutil.Runtime }

func main() {
	util.InitLogger("plugin-alb-blackhole")
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
		return p.apply(ctx, req, true)
	case model.PluginRollback:
		return p.apply(ctx, req, false)
	default:
		return model.PluginResponse{OK: false, Message: "unsupported action"}, nil
	}
}

type params struct {
	TargetGroupTags map[string]string `json:"targetGroupTags"`
	MaxTargets      int              `json:"maxTargets"`
}

func parseParams(m map[string]any) params {
	b, _ := json.Marshal(m)
	var p params
	_ = json.Unmarshal(b, &p)
	if p.MaxTargets <= 0 {
		p.MaxTargets = 1
	}
	if p.TargetGroupTags == nil {
		p.TargetGroupTags = map[string]string{}
	}
	return p
}

func (p *plugin) plan(ctx context.Context, req model.PluginRequest) (model.PluginResponse, error) {
	prm := parseParams(req.Params)
	candidates := make([]model.Target, 0)
	selectedTG := ""
	selectedAcct := ""
	selectedRegion := ""

	for _, acct := range req.Accounts {
		for _, region := range req.Regions {
			cfg, err := p.rt.AssumeTarget(ctx, acct, "alb", req.Env, region)
			if err != nil {
				return model.PluginResponse{OK: false, Message: err.Error()}, nil
			}
			elbc := elasticloadbalancingv2.NewFromConfig(cfg)
			tgOut, err := elbc.DescribeTargetGroups(ctx, &elasticloadbalancingv2.DescribeTargetGroupsInput{})
			if err != nil {
				return model.PluginResponse{OK: false, Message: err.Error()}, nil
			}
			for _, tg := range tgOut.TargetGroups {
				arn := deref(tg.TargetGroupArn)
				if arn == "" {
					continue
				}
				if len(prm.TargetGroupTags) > 0 {
					tagsOut, err := elbc.DescribeTags(ctx, &elasticloadbalancingv2.DescribeTagsInput{ResourceArns: []string{arn}})
					if err != nil {
						return model.PluginResponse{OK: false, Message: err.Error()}, nil
					}
					match := true
					if len(tagsOut.TagDescriptions) > 0 {
						m := map[string]string{}
						for _, tag := range tagsOut.TagDescriptions[0].Tags {
							m[deref(tag.Key)] = deref(tag.Value)
						}
						for k, v := range prm.TargetGroupTags {
							if m[k] != v {
								match = false
								break
							}
						}
					}
					if !match {
						continue
					}
				}
				// pick first matching TG
				selectedTG = arn
				selectedAcct = acct
				selectedRegion = region
				break
			}
			if selectedTG != "" {
				break
			}
		}
		if selectedTG != "" {
			break
		}
	}

	if selectedTG == "" {
		return model.PluginResponse{OK: false, Message: "no matching target group found"}, nil
	}

	cfg, err := p.rt.AssumeTarget(ctx, selectedAcct, "alb", req.Env, selectedRegion)
	if err != nil {
		return model.PluginResponse{OK: false, Message: err.Error()}, nil
	}
	elbc := elasticloadbalancingv2.NewFromConfig(cfg)
	health, err := elbc.DescribeTargetHealth(ctx, &elasticloadbalancingv2.DescribeTargetHealthInput{TargetGroupArn: &selectedTG})
	if err != nil {
		return model.PluginResponse{OK: false, Message: err.Error()}, nil
	}
	for _, th := range health.TargetHealthDescriptions {
		id := deref(th.Target.Id)
		port := int32(0)
		if th.Target.Port != nil {
			port = *th.Target.Port
		}
		candidates = append(candidates, model.Target{AccountID: selectedAcct, Region: selectedRegion, Type: "ALB_TARGET", ID: id, Meta: map[string]string{"targetGroupArn": selectedTG, "port": fmt.Sprintf("%d", port)}})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	if len(candidates) > prm.MaxTargets {
		candidates = candidates[:prm.MaxTargets]
	}
	state := map[string]any{"targetGroupArn": selectedTG, "targets": candidates}
	return model.PluginResponse{OK: true, Targets: candidates, State: state, Message: fmt.Sprintf("selected %d targets in TG", len(candidates))}, nil
}

func (p *plugin) apply(ctx context.Context, req model.PluginRequest, inject bool) (model.PluginResponse, error) {
	// group by TG + acct + region
	groups := map[string][]elbtypes.TargetDescription{}
	for _, t := range req.Targets {
		tg := ""
		port := int32(0)
		if t.Meta != nil {
			tg = t.Meta["targetGroupArn"]
			if t.Meta["port"] != "" {
				var tmp int
				_, _ = fmt.Sscanf(t.Meta["port"], "%d", &tmp)
				port = int32(tmp)
			}
		}
		key := fmt.Sprintf("%s|%s|%s", t.AccountID, t.Region, tg)
		id := t.ID
		p := port
		groups[key] = append(groups[key], elbtypes.TargetDescription{Id: &id, Port: &p})
	}
	for key, targets := range groups {
		parts := strings.SplitN(key, "|", 3)
		if len(parts) != 3 {
			continue
		}
		acct, region, tg := parts[0], parts[1], parts[2]
		if acct == "" || region == "" || tg == "" {
			continue
		}
		cfg, err := p.rt.AssumeTarget(ctx, acct, "alb", req.Env, region)
		if err != nil {
			return model.PluginResponse{OK: false, Message: err.Error()}, nil
		}
		elbc := elasticloadbalancingv2.NewFromConfig(cfg)
		if inject {
			_, err = elbc.DeregisterTargets(ctx, &elasticloadbalancingv2.DeregisterTargetsInput{TargetGroupArn: &tg, Targets: targets})
		} else {
			_, err = elbc.RegisterTargets(ctx, &elasticloadbalancingv2.RegisterTargetsInput{TargetGroupArn: &tg, Targets: targets})
		}
		if err != nil {
			return model.PluginResponse{OK: false, Message: err.Error()}, nil
		}
	}
	msg := "rollback"
	if inject {
		msg = "blackhole"
	}
	return model.PluginResponse{OK: true, Message: msg}, nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
