package main

import (
	"context"
	"fmt"
	"sort"

	"chaosforge/pkg/chaos/model"
	"chaosforge/pkg/chaos/pluginutil"
	"chaosforge/pkg/chaos/util"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
)

func main() {
	util.InitLogger("plugin-alb-blackhole")
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
	arns := getTGArns(req.Params)
	if len(arns) == 0 {
		return model.PluginResponse{OK: false, Message: "targetGroupArn(s) param required"}, nil
	}
	eligible := []model.Target{}
	for _, acct := range req.Accounts {
		for _, region := range req.Regions {
			cfg, err := rt.AssumeTarget(ctx, acct, "alb", req.Env, region)
			if err != nil {
				continue
			}
			elbc := elasticloadbalancingv2.NewFromConfig(cfg)
			for _, tg := range arns {
				th, err := elbc.DescribeTargetHealth(ctx, &elasticloadbalancingv2.DescribeTargetHealthInput{TargetGroupArn: aws.String(tg)})
				if err != nil {
					continue
				}
				for _, d := range th.TargetHealthDescriptions {
					// only healthy targets
					if d.TargetHealth != nil && d.TargetHealth.State != elbtypes.TargetHealthStateEnumHealthy {
						continue
					}
					id := aws.ToString(d.Target.Id)
					port := int(aws.ToInt32(d.Target.Port))
					eligible = append(eligible, model.Target{AccountID: acct, Region: region, Type: "alb-target", ID: fmt.Sprintf("%s:%d", id, port), Meta: map[string]any{"targetGroupArn": tg, "targetId": id, "port": port}})
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
	for _, t := range req.Targets {
		tg, _ := t.Meta["targetGroupArn"].(string)
		id, _ := t.Meta["targetId"].(string)
		port := intFromAny(t.Meta["port"], 0)
		if tg == "" || id == "" || port == 0 {
			continue
		}
		cfg, err := rt.AssumeTarget(ctx, t.AccountID, "alb", req.Env, t.Region)
		if err != nil {
			return model.PluginResponse{OK: false, Message: err.Error()}, nil
		}
		elbc := elasticloadbalancingv2.NewFromConfig(cfg)
		_, err = elbc.DeregisterTargets(ctx, &elasticloadbalancingv2.DeregisterTargetsInput{TargetGroupArn: aws.String(tg), Targets: []elbtypes.TargetDescription{{Id: aws.String(id), Port: aws.Int32(int32(port))}}})
		if err != nil {
			return model.PluginResponse{OK: false, Message: err.Error()}, nil
		}
	}
	return model.PluginResponse{OK: true, Message: "deregistered", State: req.State}, nil
}

func rollback(ctx context.Context, rt *pluginutil.Runtime, req model.PluginRequest) (model.PluginResponse, error) {
	for _, t := range req.Targets {
		tg, _ := t.Meta["targetGroupArn"].(string)
		id, _ := t.Meta["targetId"].(string)
		port := intFromAny(t.Meta["port"], 0)
		if tg == "" || id == "" || port == 0 {
			continue
		}
		cfg, err := rt.AssumeTarget(ctx, t.AccountID, "alb", req.Env, t.Region)
		if err != nil {
			continue
		}
		elbc := elasticloadbalancingv2.NewFromConfig(cfg)
		_, _ = elbc.RegisterTargets(ctx, &elasticloadbalancingv2.RegisterTargetsInput{TargetGroupArn: aws.String(tg), Targets: []elbtypes.TargetDescription{{Id: aws.String(id), Port: aws.Int32(int32(port))}}})
	}
	return model.PluginResponse{OK: true, Message: "registered", State: req.State}, nil
}

func intFromAny(v any, def int) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	default:
		return def
	}
}

func getTGArns(params map[string]any) []string {
	if params == nil {
		return nil
	}
	if v, ok := params["targetGroupArn"].(string); ok && v != "" {
		return []string{v}
	}
	if v, ok := params["targetGroupArns"].([]any); ok {
		out := []string{}
		for _, x := range v {
			if s, ok := x.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
