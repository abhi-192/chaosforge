package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"chaosforge/pkg/chaos/model"
	"chaosforge/pkg/chaos/store"
	"chaosforge/pkg/chaos/util"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/rs/zerolog/log"
)

type deps struct {
	store store.Store
	sfn   *sfn.Client
	ssm   *ssm.Client

	sfnArn string
}

func main() {
	util.InitLogger("api")
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		panic(err)
	}
	db := dynamodb.NewFromConfig(cfg)
	st := store.NewDynamo(db)
	st.TemplatesTable = util.MustGetenv("CHAOS_TEMPLATES_TABLE")
	st.PluginsTable = util.MustGetenv("CHAOS_PLUGINS_TABLE")
	st.RunsTable = util.MustGetenv("CHAOS_RUNS_TABLE")
	st.EventsTable = util.MustGetenv("CHAOS_EVENTS_TABLE")
	st.ApprovalsTable = util.MustGetenv("CHAOS_APPROVALS_TABLE")
	st.AccountsTable = util.MustGetenv("CHAOS_ACCOUNTS_TABLE")
	st.PoliciesTable = util.MustGetenv("CHAOS_POLICIES_TABLE")

	d := &deps{
		store: st,
		sfn:   sfn.NewFromConfig(cfg),
		ssm:   ssm.NewFromConfig(cfg),
		sfnArn: util.MustGetenv("CHAOS_SFN_ARN"),
	}
	lambda.Start(d.handler)
}

func (d *deps) handler(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	path := req.RawPath
	method := strings.ToUpper(req.RequestContext.HTTP.Method)
	log.Debug().Str("method", method).Str("path", path).Msg("request")

	switch {
	case method == "GET" && path == "/health":
		return util.JSON(200, map[string]any{"ok": true, "service": "ChaosForge API"}), nil

	case method == "POST" && path == "/runs":
		return d.postRun(ctx, req), nil
	case method == "GET" && strings.HasPrefix(path, "/runs/"):
		id := strings.TrimPrefix(path, "/runs/")
		return d.getRun(ctx, id), nil

	case method == "POST" && strings.HasPrefix(path, "/approvals/"):
		// /approvals/{runId}/approve|reject
		parts := strings.Split(strings.TrimPrefix(path, "/approvals/"), "/")
		if len(parts) != 2 {
			return util.Err(404, "not found"), nil
		}
		runID := parts[0]
		action := parts[1]
		switch action {
		case "approve":
			return d.approve(ctx, req, runID, true), nil
		case "reject":
			return d.approve(ctx, req, runID, false), nil
		default:
			return util.Err(404, "not found"), nil
		}

	case method == "POST" && path == "/killswitch/engage":
		return d.setKillSwitch(ctx, req, "ENGAGED"), nil
	case method == "POST" && path == "/killswitch/disengage":
		return d.setKillSwitch(ctx, req, "DISENGAGED"), nil

	default:
		return util.Err(404, "not found"), nil
	}
}

type postRunReq struct {
	TemplateID      string         `json:"templateId"`
	TemplateVersion int            `json:"templateVersion"`
	Env             string         `json:"env"`
	Accounts        []string       `json:"accounts"`
	Regions         []string       `json:"regions"`
	Params          map[string]any `json:"params"`
}

func (d *deps) postRun(ctx context.Context, req events.APIGatewayV2HTTPRequest) events.APIGatewayV2HTTPResponse {
	var in postRunReq
	if err := json.Unmarshal([]byte(req.Body), &in); err != nil {
		return util.Err(400, "invalid json")
	}
	if in.TemplateID == "" || in.TemplateVersion == 0 || in.Env == "" {
		return util.Err(400, "templateId, templateVersion, env are required")
	}
	if len(in.Accounts) == 0 || len(in.Regions) == 0 {
		return util.Err(400, "accounts and regions are required")
	}
	pol, err := d.store.GetLatestPolicy(ctx, in.Env)
	if err != nil {
		return util.Err(400, fmt.Sprintf("policy not found for env %s", in.Env))
	}
	tpl, err := d.store.GetTemplate(ctx, in.TemplateID, in.TemplateVersion)
	if err != nil {
		return util.Err(400, "template not found")
	}
	pluginID, pluginVer, err := model.ParsePluginRef(tpl.PluginRef)
	if err != nil {
		return util.Err(500, err.Error())
	}
	// allowedPlugins check
	allowed := false
	for _, p := range pol.AllowedPlugins {
		if p == pluginID {
			allowed = true
			break
		}
	}
	if !allowed {
		return util.Err(403, "plugin not allowed by policy")
	}

	runID := util.NewRunID()
	who := actorFromReq(req)
	now := time.Now().UTC()

	r := &model.ChaosRun{
		RunID:            runID,
		TemplateID:       in.TemplateID,
		TemplateVersion:  in.TemplateVersion,
		Env:              in.Env,
		RequestedBy:      who,
		RequestedAt:      now,
		State:            model.RunReady,
		Regions:          in.Regions,
		Accounts:         in.Accounts,
		Params:           in.Params,
		PolicySnapshot:   utilMap(pol),
		ApprovalRequired: pol.RequireApproval,
		EvidencePrefix:   fmt.Sprintf("runs/%s", runID),
		PluginState:      map[string]any{},
	}
	if pol.RequireApproval {
		r.State = model.RunPendingApproval
	}
	if err := d.store.PutRun(ctx, r); err != nil {
		log.Error().Err(err).Msg("put run")
		return util.Err(500, "failed to create run")
	}
	_ = d.store.PutEvent(ctx, &model.ChaosRunEvent{
		RunID:   runID,
		Type:    model.EventPlan,
		Message: fmt.Sprintf("Run created by %s using %s@%d", who, pluginID, pluginVer),
		Details: map[string]any{"templateId": in.TemplateID, "templateVersion": in.TemplateVersion},
	})

	// start Step Functions execution
	input := fmt.Sprintf("{\"runId\":%q}", runID)
	exec, err := d.sfn.StartExecution(ctx, &sfn.StartExecutionInput{
		StateMachineArn: aws.String(d.sfnArn),
		Name:            aws.String(runID),
		Input:           aws.String(input),
	})
	if err != nil {
		log.Error().Err(err).Msg("start execution")
		return util.Err(500, "failed to start orchestrator")
	}
	r.SfnExecutionArn = aws.ToString(exec.ExecutionArn)
	if err := d.store.UpdateRun(ctx, r); err != nil {
		log.Error().Err(err).Msg("update run")
	}

	return util.JSON(202, map[string]any{"runId": runID, "executionArn": r.SfnExecutionArn})
}

func (d *deps) getRun(ctx context.Context, runID string) events.APIGatewayV2HTTPResponse {
	r, err := d.store.GetRun(ctx, runID)
	if err != nil {
		return util.Err(404, "run not found")
	}
	evs, _ := d.store.ListEvents(ctx, runID, 200)
	return util.JSON(200, map[string]any{"run": r, "events": evs})
}

func (d *deps) approve(ctx context.Context, req events.APIGatewayV2HTTPRequest, runID string, approve bool) events.APIGatewayV2HTTPResponse {
	ap, err := d.store.GetApproval(ctx, runID)
	if err != nil {
		return util.Err(404, "approval not found")
	}
	if ap.State != model.ApprovalPending {
		return util.Err(409, "approval is not pending")
	}

	actor := actorFromReq(req)
	comment := ""
	if req.Body != "" {
		var body map[string]any
		_ = json.Unmarshal([]byte(req.Body), &body)
		if v, ok := body["comment"].(string); ok {
			comment = v
		}
	}
	decision := model.ApprovalDecision{Actor: actor, At: time.Now().UTC(), Comment: comment}
	if approve {
		decision.Decision = "APPROVED"
		_, err = d.sfn.SendTaskSuccess(ctx, &sfn.SendTaskSuccessInput{TaskToken: aws.String(ap.TaskToken), Output: aws.String("{\"approved\":true}")})
		ap.State = model.ApprovalApproved
	} else {
		decision.Decision = "REJECTED"
		_, err = d.sfn.SendTaskFailure(ctx, &sfn.SendTaskFailureInput{TaskToken: aws.String(ap.TaskToken), Error: aws.String("REJECTED"), Cause: aws.String(comment)})
		ap.State = model.ApprovalRejected
	}
	if err != nil {
		log.Error().Err(err).Msg("send task")
		return util.Err(500, "failed to respond to approval")
	}
	ap.Decisions = append(ap.Decisions, decision)
	if err := d.store.UpdateApproval(ctx, ap); err != nil {
		log.Error().Err(err).Msg("update approval")
	}
	_ = d.store.PutEvent(ctx, &model.ChaosRunEvent{RunID: runID, Type: model.EventMonitor, Message: fmt.Sprintf("Approval %s by %s", decision.Decision, actor)})

	return util.JSON(200, map[string]any{"ok": true, "state": ap.State})
}

func (d *deps) setKillSwitch(ctx context.Context, req events.APIGatewayV2HTTPRequest, value string) events.APIGatewayV2HTTPResponse {
	q := parseQuery(req.RawQueryString)
	env := q.Get("env")
	if env == "" {
		return util.Err(400, "env query param is required")
	}
	name := fmt.Sprintf("/chaos/%s/killswitch", env)
	// Spec path: /chaos/<env>/kill-switch
	name = fmt.Sprintf("/chaos/%s/kill-switch", env)
	_, err := d.ssm.PutParameter(ctx, &ssm.PutParameterInput{Name: aws.String(name), Value: aws.String(value), Type: "String", Overwrite: aws.Bool(true)})
	if err != nil {
		log.Error().Err(err).Msg("put parameter")
		return util.Err(500, "failed to set kill-switch")
	}
	return util.JSON(200, map[string]any{"ok": true, "env": env, "value": value})
}

func parseQuery(raw string) url.Values {
	v, _ := url.ParseQuery(raw)
	return v
}

func actorFromReq(req events.APIGatewayV2HTTPRequest) string {
	if req.Headers != nil {
		if v := req.Headers["x-actor"]; v != "" {
			return v
		}
	}
	// Best-effort for IAM auth
	if req.RequestContext.Authorizer.IAM.UserArn != "" {
		return req.RequestContext.Authorizer.IAM.UserArn
	}
	if req.RequestContext.Identity.UserArn != "" {
		return req.RequestContext.Identity.UserArn
	}
	return "unknown"
}

func utilMap(p *model.ChaosPolicy) map[string]any {
	if p == nil {
		return map[string]any{}
	}
	b, _ := json.Marshal(p)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

var _ = errors.New
