# ChaosForge (Go-only) – Chaos Engineering Framework for AWS

This repo contains a **Go-only** implementation of a multi-account, policy-gated chaos framework:

- Control plane (API + Step Functions orchestrator + DynamoDB)
- Target account baseline (StackSet roles)
- Pluggable chaos plugins (EC2/SSM/ALB)
- Evidence reporting to an encrypted S3 bucket

> Runtime assumption: plugins run in the control-plane account and **assume roles** in target accounts using `ExternalId = <OrgName>/<Env>`.

## Quick start

### 1) Build & package Lambdas

```bash
make tidy
make package
```

You’ll get zip artifacts under `dist/*.zip`.

### 2) Upload artifacts to S3

Upload all `dist/*.zip` into your artifacts bucket under the configured prefix:

```bash
aws s3 cp dist/ s3://YOUR_ARTIFACTS_BUCKET/YOUR_PREFIX/ --recursive --exclude "*" --include "*.zip"
```

Artifact names expected by the control-plane stack:

- `api.zip`
- `planner.zip`
- `alarmchecker.zip`
- `approvalwaiter.zip`
- `pluginexecutor.zip`
- `monitor.zip`
- `reporter.zip`
- `abort.zip`
- `plugin-ec2-stop.zip`
- `plugin-ec2-terminate.zip`
- `plugin-ssm-netem.zip`
- `plugin-ssm-stressng.zip`
- `plugin-alb-blackhole.zip`

### 3) Deploy control plane (CloudFormation)

```bash
aws cloudformation deploy \
  --stack-name ChaosForge-ControlPlane \
  --template-file infra/cfn/control-plane.yaml \
  --capabilities CAPABILITY_NAMED_IAM \
  --parameter-overrides \
      OrgName=AcmeCorp \
      ArtifactsBucket=YOUR_ARTIFACTS_BUCKET \
      ArtifactsPrefix=YOUR_PREFIX
```

Grab the API endpoint:

```bash
aws cloudformation describe-stacks --stack-name ChaosForge-ControlPlane \
  --query 'Stacks[0].Outputs[?OutputKey==`ApiEndpoint`].OutputValue' --output text
```

### 4) Deploy target baseline (StackSet)

Use `infra/cfn/target-baseline.yaml` as a StackSet template and deploy it to the desired OUs and regions.

Parameters:
- `ControlPlaneAccountId`: the control plane AWS account ID
- `OrgName`: must match control plane `OrgName`
- `RolePrefix`: default `ChaosTargetExecutionRole`

### 5) Seed metadata (Policies, Accounts, Plugins, Templates)

Policies/templates/plugins are stored in DynamoDB.

- Policies: `${StackName}-ChaosPolicies`
- Templates: `${StackName}-ChaosTemplates`
- Plugins: `${StackName}-ChaosPlugins`
- Accounts: `${StackName}-ChaosAccounts`

A complete example config (including sample policies/plugins/templates) is in:

- `config/chaosforge.yaml.example` (recommended)
- `configs/example.yaml` (older example)

ChaosForge includes a small **seeder CLI** that reads your YAML config and seeds the DynamoDB tables using the
**CloudFormation outputs** from your control-plane stack:

```bash
go build -o bin/chaosforge ./cmd/chaosforge

./bin/chaosforge seed --config config/chaosforge.yaml.example \
  --stack ChaosForge-ControlPlane
```

### 5.1) Validate everything (recommended)

ChaosForge includes **chaosctl** to validate:

- config semantics (policies/plugins/templates)
- control-plane stack outputs
- DynamoDB tables + seeded items
- StackSet rollout (SERVICE_MANAGED)
- cross-account role assumption + minimal permission smoke tests

```bash
go build -o bin/chaosctl ./cmd/chaosctl

./bin/chaosctl validate --config config/chaosforge.yaml.example \
  --stack ChaosForge-ControlPlane
```

### 6) Initialize kill-switch parameter(s)

The orchestrator reads: `/chaos/<env>/kill-switch`.

The seeder automatically creates/overwrites kill-switch params for each env found under `policies:`.
If you want to do it manually, initialize per env:

```bash
aws ssm put-parameter --name /chaos/dev/kill-switch --type String --value DISENGAGED --overwrite
aws ssm put-parameter --name /chaos/prod/kill-switch --type String --value DISENGAGED --overwrite
```

## API (HTTP API, AWS_IAM auth)

- `GET /health`
- `POST /runs`
- `GET /runs/{runId}`
- `POST /approvals/{runId}/approve`
- `POST /approvals/{runId}/reject`
- `POST /killswitch/engage` (body: `{ "env": "prod" }`)
- `POST /killswitch/disengage` (body: `{ "env": "prod" }`)

## Plugins

Implemented plugins:

- `ec2-stop@1` – stop/start EC2 instances
- `ec2-terminate@1` – terminate instances (rollback via ASG desired capacity bump if ASG-managed)
- `ssm-netem@1` – inject/rollback netem latency/loss (tc)
- `ssm-stressng@1` – inject/rollback cpu/mem/io stress
- `alb-blackhole@1` – deregister/register targets from a target group

## Notes

- This repo is intended as a contractor-ready baseline. Tighten IAM, add more plugins, and add richer validation/audit as needed.
- Preflight and postcheck alarm checks use CloudWatch alarm state (OK vs ALARM/INSUFFICIENT_DATA).
