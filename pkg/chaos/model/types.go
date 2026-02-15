package model

import "time"

// DynamoDB table names (default, per spec). Actual physical names are set by CFN; we keep defaults.
const (
	TableTemplates  = "ChaosTemplates"
	TablePlugins    = "ChaosPlugins"
	TableRuns       = "ChaosRuns"
	TableEvents     = "ChaosRunEvents"
	TableApprovals  = "ChaosApprovals"
	TableAccounts   = "ChaosAccounts"
	TablePolicies   = "ChaosPolicies"
)

type TemplateStatus string

const (
	TemplateDraft      TemplateStatus = "DRAFT"
	TemplateApproved   TemplateStatus = "APPROVED"
	TemplateDeprecated TemplateStatus = "DEPRECATED"
)

type RunState string

const (
	RunPendingApproval RunState = "PENDING_APPROVAL"
	RunReady           RunState = "READY"
	RunRunning         RunState = "RUNNING"
	RunRollingBack     RunState = "ROLLING_BACK"
	RunSucceeded       RunState = "SUCCEEDED"
	RunFailed          RunState = "FAILED"
	RunAborted         RunState = "ABORTED"
)

type EventType string

const (
	EventPlan      EventType = "PLAN"
	EventPrecheck  EventType = "PRECHECK"
	EventInject    EventType = "INJECT"
	EventMonitor   EventType = "MONITOR"
	EventAlarm     EventType = "ALARM"
	EventKillSwitch EventType = "KILLSWITCH"
	EventRollback  EventType = "ROLLBACK"
	EventPostcheck EventType = "POSTCHECK"
	EventFinal     EventType = "FINAL"
)

type ApprovalState string

const (
	ApprovalPending  ApprovalState = "PENDING"
	ApprovalApproved ApprovalState = "APPROVED"
	ApprovalRejected ApprovalState = "REJECTED"
	ApprovalExpired  ApprovalState = "EXPIRED"
)

type ChaosTemplate struct {
	TemplateID     string         `dynamodbav:"templateId" json:"templateId"`
	Version        int            `dynamodbav:"version" json:"version"`
	Name           string         `dynamodbav:"name" json:"name"`
	Description    string         `dynamodbav:"description" json:"description"`
	EnvScope       string         `dynamodbav:"envScope" json:"envScope"`
	PluginRef      string         `dynamodbav:"pluginRef" json:"pluginRef"` // pluginId@version
	PluginID       string         `dynamodbav:"pluginId" json:"pluginId"`
	ParamsSchema   map[string]any `dynamodbav:"paramsSchema" json:"paramsSchema"`
	DefaultParams  map[string]any `dynamodbav:"defaultParams" json:"defaultParams"`
	RequiredAlarms []string       `dynamodbav:"requiredAlarms" json:"requiredAlarms"`
	StopAlarms     []string       `dynamodbav:"stopAlarms" json:"stopAlarms"`
	ReportConfig   map[string]any `dynamodbav:"reportConfig" json:"reportConfig"`
	Status         TemplateStatus `dynamodbav:"status" json:"status"`
	CreatedBy      string         `dynamodbav:"createdBy" json:"createdBy"`
	CreatedAt      time.Time      `dynamodbav:"createdAt" json:"createdAt"`
	UpdatedAt      time.Time      `dynamodbav:"updatedAt" json:"updatedAt"`
}

type ChaosPlugin struct {
	PluginID            string         `dynamodbav:"pluginId" json:"pluginId"`
	Version             int            `dynamodbav:"version" json:"version"`
	LambdaArn           string         `dynamodbav:"lambdaArn" json:"lambdaArn"`
	Description         string         `dynamodbav:"description" json:"description"`
	Capabilities        []string       `dynamodbav:"capabilities" json:"capabilities"`
	RequiredTargetRole  string         `dynamodbav:"requiredTargetRole" json:"requiredTargetRole"` // ec2|ssm|alb
	ParamSchema         map[string]any `dynamodbav:"paramSchema" json:"paramSchema"`
	Status              string         `dynamodbav:"status" json:"status"`
	CreatedAt           time.Time      `dynamodbav:"createdAt" json:"createdAt"`
}

type ChaosRun struct {
	RunID            string                 `dynamodbav:"runId" json:"runId"`
	TemplateID       string                 `dynamodbav:"templateId" json:"templateId"`
	TemplateVersion  int                    `dynamodbav:"templateVersion" json:"templateVersion"`
	Env              string                 `dynamodbav:"env" json:"env"`
	RequestedBy      string                 `dynamodbav:"requestedBy" json:"requestedBy"`
	RequestedAt      time.Time              `dynamodbav:"requestedAt" json:"requestedAt"`
	State            RunState               `dynamodbav:"state" json:"state"`
	Targets          any                    `dynamodbav:"targets" json:"targets"` // summary or structured
	Regions          []string               `dynamodbav:"regions" json:"regions"`
	Accounts         []string               `dynamodbav:"accounts" json:"accounts"`
	Params           map[string]any         `dynamodbav:"params" json:"params"`
	PolicySnapshot   map[string]any         `dynamodbav:"policySnapshot" json:"policySnapshot"`
	ApprovalRequired bool                   `dynamodbav:"approvalRequired" json:"approvalRequired"`
	SfnExecutionArn  string                 `dynamodbav:"sfnExecutionArn" json:"sfnExecutionArn"`
	StartedAt        *time.Time             `dynamodbav:"startedAt" json:"startedAt"`
	EndedAt          *time.Time             `dynamodbav:"endedAt" json:"endedAt"`
	Outcome          string                 `dynamodbav:"outcome" json:"outcome"`
	Rollback         map[string]any         `dynamodbav:"rollback" json:"rollback"`
	EvidencePrefix   string                 `dynamodbav:"evidencePrefix" json:"evidencePrefix"`
	PluginState      map[string]any         `dynamodbav:"pluginState" json:"pluginState"`
}

type ChaosRunEvent struct {
	RunID     string                 `dynamodbav:"runId" json:"runId"`
	SK        string                 `dynamodbav:"sk" json:"sk"` // ts#<epochMillis>#<seq>
	Type      EventType              `dynamodbav:"type" json:"type"`
	Message   string                 `dynamodbav:"message" json:"message"`
	Details   map[string]any         `dynamodbav:"details" json:"details"`
	PluginID  string                 `dynamodbav:"pluginId" json:"pluginId"`
	AccountID string                 `dynamodbav:"accountId" json:"accountId"`
	Region    string                 `dynamodbav:"region" json:"region"`
}

type ChaosApproval struct {
	ApprovalID  string          `dynamodbav:"approvalId" json:"approvalId"` // runId
	RunID       string          `dynamodbav:"runId" json:"runId"`
	State       ApprovalState   `dynamodbav:"state" json:"state"`
	TaskToken   string          `dynamodbav:"taskToken" json:"taskToken"`
	RequestedAt time.Time       `dynamodbav:"requestedAt" json:"requestedAt"`
	Decisions   []ApprovalDecision `dynamodbav:"decisions" json:"decisions"`
	TTL         int64           `dynamodbav:"ttl" json:"ttl"`
}

type ApprovalDecision struct {
	Actor     string    `dynamodbav:"actor" json:"actor"`
	Decision  string    `dynamodbav:"decision" json:"decision"` // APPROVED|REJECTED
	At        time.Time `dynamodbav:"at" json:"at"`
	Comment   string    `dynamodbav:"comment" json:"comment"`
}

type ChaosAccount struct {
	AccountID       string            `dynamodbav:"accountId" json:"accountId"`
	OUPath          string            `dynamodbav:"ouPath" json:"ouPath"`
	Enabled         bool              `dynamodbav:"enabled" json:"enabled"`
	AllowedRegions  []string          `dynamodbav:"allowedRegions" json:"allowedRegions"`
	RoleNames       map[string]string `dynamodbav:"roleNames" json:"roleNames"` // pluginType->roleName
	Tags            map[string]string `dynamodbav:"tags" json:"tags"`
	LastValidatedAt *time.Time        `dynamodbav:"lastValidatedAt" json:"lastValidatedAt"`
}

type ChaosPolicy struct {
	Env                 string         `dynamodbav:"env" json:"env"`
	Version             int            `dynamodbav:"version" json:"version"`
	BlastRadiusLimits   map[string]any `dynamodbav:"blastRadiusLimits" json:"blastRadiusLimits"`
	AllowedPlugins      []string       `dynamodbav:"allowedPlugins" json:"allowedPlugins"`
	FreezeWindows       []any          `dynamodbav:"freezeWindows" json:"freezeWindows"`
	RequireApproval     bool           `dynamodbav:"requireApproval" json:"requireApproval"`
	RequiredTags        map[string]string `dynamodbav:"requiredTags" json:"requiredTags"`
	DenyTags            map[string]string `dynamodbav:"denyTags" json:"denyTags"`
	MaxRunDurationSeconds int          `dynamodbav:"maxRunDurationSeconds" json:"maxRunDurationSeconds"`
	PreflightWindowSeconds int         `dynamodbav:"preflightWindowSeconds" json:"preflightWindowSeconds"`
	CreatedAt           time.Time      `dynamodbav:"createdAt" json:"createdAt"`
}

// Plugin contract payload

type PluginAction string

const (
	PluginPlan      PluginAction = "plan"
	PluginPrecheck  PluginAction = "precheck"
	PluginInject    PluginAction = "inject"
	PluginRollback  PluginAction = "rollback"
	PluginPostcheck PluginAction = "postcheck"
)

type PluginRequest struct {
	RunID   string                 `json:"runId"`
	Action  PluginAction           `json:"action"`
	Env     string                 `json:"env"`
	Params  map[string]any         `json:"params"`
	Accounts []string              `json:"accounts"`
	Regions  []string              `json:"regions"`
	Targets  []Target              `json:"targets"`
	Policy   map[string]any        `json:"policy"`
}

type PluginResponse struct {
	OK     bool              `json:"ok"`
	State  map[string]any    `json:"state"`
	Events []ChaosRunEvent   `json:"events"`
	Targets []Target         `json:"targets"` // for plan
	Message string           `json:"message"`
}

type Target struct {
	AccountID string            `json:"accountId"`
	Region    string            `json:"region"`
	Type      string            `json:"type"`
	ID        string            `json:"id"`
	Meta      map[string]string `json:"meta"`
}
