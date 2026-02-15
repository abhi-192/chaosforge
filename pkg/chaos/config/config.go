package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	OrgName          string            `yaml:"orgName"`
	ControlPlane     ControlPlane      `yaml:"controlPlane"`
	StackSets        StackSets         `yaml:"stackSets"`
	TargetAccounts   []TargetAccount   `yaml:"targetAccounts"`
	ExternalIDFormat string            `yaml:"externalIdFormat"`
	Policies         map[string]Policy `yaml:"policies"` // env -> policy
	Plugins          []PluginDef       `yaml:"plugins"`
	Templates        []TemplateSeed    `yaml:"templates"`
}

type ControlPlane struct {
	AccountID string `yaml:"accountId"`
	Region    string `yaml:"region"`
	StackName string `yaml:"stackName"`
	Artifacts struct {
		Bucket string `yaml:"bucket"`
		Prefix string `yaml:"prefix"`
	} `yaml:"artifacts"`
	Evidence struct {
		Bucket    string `yaml:"bucket"`
		KMSKeyArn string `yaml:"kmsKeyArn"`
	} `yaml:"evidence"`
	ApiEndpoint string `yaml:"apiEndpoint"` // filled after deploy
}

type StackSets struct {
	PermissionModel       string   `yaml:"permissionModel"`
	StackSetName          string   `yaml:"stackSetName"`
	OrganizationalUnitIDs []string `yaml:"organizationalUnitIds"`
	Regions               []string `yaml:"regions"`
	AutoDeployment        struct {
		Enabled                      bool `yaml:"enabled"`
		RetainStacksOnAccountRemoval bool `yaml:"retainStacksOnAccountRemoval"`
	} `yaml:"autoDeployment"`
	ManagedExecution struct {
		Active bool `yaml:"active"`
	} `yaml:"managedExecution"`
}

type TargetAccount struct {
	AccountID      string            `yaml:"accountId"`
	OUPath         string            `yaml:"ouPath"`
	Enabled        bool              `yaml:"enabled"`
	AllowedRegions []string          `yaml:"allowedRegions"`
	RoleNames      map[string]string `yaml:"roleNames"` // pluginType->roleName
	Tags           map[string]string `yaml:"tags"`
}

type Policy struct {
	Version                int                         `yaml:"version"`
	RequireApproval        bool                        `yaml:"requireApproval"`
	MaxRunDurationSeconds  int                         `yaml:"maxRunDurationSeconds"`
	PreflightWindowSeconds int                         `yaml:"preflightWindowSeconds"`
	BlastRadiusLimits      map[string]BlastRadiusLimit `yaml:"blastRadiusLimits"`
	AllowedPlugins         []string                    `yaml:"allowedPlugins"`
	RequiredTags           map[string]string           `yaml:"requiredTags"`
	DenyTags               map[string]string           `yaml:"denyTags"`
	FreezeWindows          []FreezeWindow              `yaml:"freezeWindows"`
}

type BlastRadiusLimit struct {
	MaxCount   int `yaml:"maxCount"`
	MaxPercent int `yaml:"maxPercent"`
}

type FreezeWindow struct {
	Name       string   `yaml:"name"`
	TZ         string   `yaml:"tz"`
	DaysOfWeek []string `yaml:"daysOfWeek"`
	Start      string   `yaml:"start"`
	End        string   `yaml:"end"`
}

type PluginDef struct {
	PluginID               string `yaml:"pluginId"`
	Version                int    `yaml:"version"`
	LambdaName             string `yaml:"lambdaName"`
	TimeoutSeconds         int    `yaml:"timeoutSeconds"`
	MemoryMB               int    `yaml:"memoryMB"`
	RequiredTargetRoleType string `yaml:"requiredTargetRoleType"` // ec2|ssm|alb
}

type TemplateSeed struct {
	TemplateID     string         `yaml:"templateId"`
	Version        int            `yaml:"version"`
	Name           string         `yaml:"name"`
	Description    string         `yaml:"description"`
	EnvScope       string         `yaml:"envScope"`  // dev|prod|any
	Status         string         `yaml:"status"`    // DRAFT|APPROVED|DEPRECATED
	PluginRef      string         `yaml:"pluginRef"` // <pluginId>@<version>
	RequiredAlarms []string       `yaml:"requiredAlarms"`
	StopAlarms     []string       `yaml:"stopAlarms"`
	DefaultParams  map[string]any `yaml:"defaultParams"`
	ParamsSchema   map[string]any `yaml:"paramsSchema"`
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if err := c.NormalizeAndValidate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) NormalizeAndValidate() error {
	if strings.TrimSpace(c.OrgName) == "" {
		return errors.New("orgName is required")
	}
	pm := normalizeEnum(c.StackSets.PermissionModel)
	switch pm {
	case "SERVICE_MANAGED", "SELF_MANAGED":
		c.StackSets.PermissionModel = pm
	default:
		return fmt.Errorf("stackSets.permissionModel must be service_managed or self_managed (got %q)", c.StackSets.PermissionModel)
	}
	if pm != "SERVICE_MANAGED" {
		return fmt.Errorf("this build expects SERVICE_MANAGED stacksets; got %q", pm)
	}
	if c.ControlPlane.Region == "" {
		return errors.New("controlPlane.region is required")
	}
	if c.ControlPlane.StackName == "" {
		c.ControlPlane.StackName = "ChaosForge-ControlPlane"
	}
	if c.ControlPlane.Artifacts.Bucket == "" {
		return errors.New("controlPlane.artifacts.bucket is required")
	}
	if c.ControlPlane.Artifacts.Prefix == "" {
		c.ControlPlane.Artifacts.Prefix = "chaosforge"
	}
	if c.StackSets.StackSetName == "" {
		c.StackSets.StackSetName = "ChaosTargetBaseline"
	}
	if len(c.StackSets.OrganizationalUnitIDs) == 0 {
		return errors.New("stackSets.organizationalUnitIds is required for SERVICE_MANAGED")
	}
	if len(c.StackSets.Regions) == 0 {
		return errors.New("stackSets.regions is required")
	}
	if c.ExternalIDFormat == "" {
		c.ExternalIDFormat = "${ORG}/${ENV}"
	}
	if len(c.Policies) == 0 {
		return errors.New("policies is required")
	}
	if len(c.Plugins) == 0 {
		return errors.New("plugins is required")
	}
	return nil
}

func normalizeEnum(s string) string {
	ss := strings.ToUpper(strings.TrimSpace(s))
	ss = strings.ReplaceAll(ss, "-", "_")
	ss = strings.ReplaceAll(ss, " ", "_")
	return ss
}
