# Agent Identity Authentication for Sandbox OSS Mounts

This document describes how agent-identity auth works end-to-end for OSS storage in the sandbox scenario. It is background context for CSI development — not a user-facing doc.

## Overview

Agent identity replaces AK/SK-based auth for sandbox OSS mounts. Instead of static credentials in a Secret, each sandbox gets an identity token issued by the `ack-agent-identity` controller, and ossfs uses that token to obtain short-lived STS credentials scoped to the sandbox's declared permissions.

## Components

| Component | Role |
|---|---|
| `ack-agent-identity` | Issues sandbox tokens, serves credential-provider API (STS token exchange) |
| `ack-agent-sandbox-controller` | Injects CSI sidecar, manages SandboxClaim lifecycle, maps claim fields to CSI volumeAttributes |
| CSI driver (csi-plugin) | Parses `authType=agent-identity` + `sandboxId` + `sandboxCredProviderName` from volumeAttributes, passes as ossfs mount options |
| ossfs (mount-proxy) | Reads token file, calls credential-provider to get STS credentials, signs OSS requests |

## Data Flow

```
1. SRE creates:
   - PV with authType=agent-identity, bucket, url
   - AgentIdentity CR (identity for the agent)
   - CredentialProvider CR (defines RAM role + policy template)
   - AgentRole + AgentRoleBinding (RBAC: which identity can use which CredentialProvider)

2. User creates SandboxClaim with:
   - labels: security.agents.kruise.io/agent-name: <identity>
   - dynamicVolumesMount: pvName, mountPath, subPath
   - attributes: credentialProviderName: <cred-provider-name>

3. sandbox-controller:
   - Claims a Sandbox from SandboxSet
   - Sets label security.agents.kruise.io/agent-name on the pod
   - Sets label security.agents.kruise.io/storage-auth with JSON:
     [{"credentialProviderName":"<name>","attributes":{"bucket-name":"<bucket>","sub-path":"<path>"}}]
   - Injects sandboxId and sandboxCredProviderName into CSI volumeAttributes
   - Triggers CSI NodePublish

4. ack-agent-identity controller:
   - Watches pods with agent-name label
   - Issues token file at /var/opt/sandbox/agent-token/<sandboxId>.token
   - Mounts CA cert at /etc/ssl/certs/agent-identity/ca.crt (or SSL_CERT_FILE)
   - Token JSON format:
     {"requestId":"...","accessToken":"...","sandboxClientId":"...","accessTokenExpiration":"..."}

5. CSI NodePublish:
   - Parses volumeAttributes: authType, sandboxId, sandboxCredProviderName
   - Generates mount options:
     - agent_identity_endpoint=https://credential-provider.ack-agent-identity.svc:8443/
     - agent_identity_token_file=/var/opt/sandbox/agent-token/<sandboxId>.token
     - agent_identity_cred_provider=<credProviderName>
   - mount-proxy ApplyOptionDefaults appends agent_identity_ca_file if readable

6. ossfs at mount time:
   - Reads token file → gets accessToken + sandboxClientId
   - POST to credential-provider endpoint:
     Body: {"credentialType":"stsToken","resourceId":"<sandboxClientId>","credentialProviderName":"<name>"}
     Header: Authorization: Bearer <accessToken>
   - Response: {"requestId":"...","stsToken":{"accessKeyId":"...","accessKeySecret":"...","securityToken":"...","expiration":"..."}}
   - Uses STS credentials to sign OSS requests (V4 signature)
   - Periodically refreshes before expiration
```

## Key Code Paths (CSI driver)

### volumeAttribute parsing

`pkg/oss/utils.go` — parsed from PV volumeAttributes:
```
case "authtype":      → opts.AuthType (e.g. "agent-identity")
case "sandboxid":     → opts.SandboxId
case "sandboxcredprovidername": → opts.SandboxCredProviderName
```

### Auth type constants

`pkg/mounter/fuse_pod_manager/oss/oss_fuse_pod_manager.go`:
```go
AuthTypeAgentIdentity = "agent-identity"
```

### Options struct fields

`pkg/mounter/fuse_pod_manager/oss/oss_fuse_pod_manager.go`:
```go
SandboxId               string `json:"sandboxId"`
SandboxCredProviderName string `json:"sandboxCredProviderName"`
```

### Precheck

`pkg/mounter/fuse_pod_manager/oss/ossfs/manager.go` — `PrecheckAuthConfig`:
- Validates `sandboxId` is non-empty
- Validates `sandboxCredProviderName` is non-empty

### Auth config construction

`pkg/mounter/fuse_pod_manager/oss/ossfs/manager.go` — `MakeAuthConfig`:
```go
case ossfpm.AuthTypeAgentIdentity:
    authCfg.AgentIdentityConfig = &fpm.AgentIdentityConfig{
        CredProviderName: o.SandboxCredProviderName,
        SandboxId:        o.SandboxId,
    }
```

### Mount options

`pkg/mounter/fuse_pod_manager/oss/ossfs/manager.go` — `getAuthOptions`:
```go
case ossfpm.AuthTypeAgentIdentity:
    mountOptions = append(mountOptions,
        "agent_identity_endpoint="+ossfpm.GetAgentIdentityEndpoint(),
        "agent_identity_token_file="+ossfpm.GetAgentIdentityTokenFilePath(o.SandboxId),
        "agent_identity_cred_provider="+o.SandboxCredProviderName,
    )
```

### Pod spec (fuse pod / runc path)

`pkg/mounter/fuse_pod_manager/oss/ossfs/manager.go` — `buildAuthSpec`:
```go
case ossfpm.AuthTypeAgentIdentity:
    // No volumes/mounts needed — token/CA files placed by external controller
```

### Mount-proxy defaults

`pkg/mounter/proxy/server/ossfs/driver.go` — `ApplyOptionDefaults`:
- Checks if CA file is readable at `GetAgentIdentityCAFilePath()`
- If yes, appends `agent_identity_ca_file=<path>`

`pkg/mounter/proxy/server/utils.go`:
```go
const AgentIdentityCAFilePath = "/etc/ssl/certs/agent-identity/ca.crt"
// GetAgentIdentityCAFilePath prefers SSL_CERT_FILE env var, falls back to AgentIdentityCAFilePath
```

### Utility functions

`pkg/mounter/fuse_pod_manager/oss/utils.go`:
```go
func GetAgentIdentityTokenFilePath(sandboxId string) string {
    return fmt.Sprintf("/var/opt/sandbox/agent-token/%s.token", sandboxId)
}

func GetAgentIdentityEndpoint() string {
    if ep := os.Getenv("AGENT_IDENTITY_ENDPOINT"); ep != "" { return ep }
    return "https://credential-provider.ack-agent-identity.svc:8443/"
}
```

## ossfs2 Status

ossfs2 does NOT support agent-identity auth. The `PrecheckAuthConfig` in `ossfs2/manager.go` returns error for any unrecognized authType. The mount-proxy `ossfs2/driver.go` `ApplyOptionDefaults` is a no-op.

## CredentialProvider CR

Defines the RAM role and scoped policy for STS credentials. Example:

```yaml
apiVersion: agentidentity.alibabacloud.com/v1alpha1
kind: CredentialProvider
metadata:
  name: oss-readonly
  namespace: default
spec:
  type: RAM
  ram:
    source:
      provider: RRSA
      rrsa:
        roleName: ack-agent-identity-oss-role
        policy: |
          {
            "Statement": [
              {
                "Action": ["oss:ListObjects", "oss:GetObject"],
                "Effect": "Allow",
                "Resource": [
                  "acs:oss:*:*:${ack:agent-identity/storage-auth/bucket-name}/${ack:agent-identity/storage-auth/sub-path}/*"
                ]
              }
            ],
            "Version": "1"
          }
```

### Template Variables

Policy supports template variables resolved per-sandbox from the `storage-auth` metadata label:

| Variable | Source |
|---|---|
| `${ack:agent-identity/storage-auth/bucket-name}` | From storage-auth label attributes |
| `${ack:agent-identity/storage-auth/sub-path}` | From storage-auth label attributes |
| `${ack:agent-identity/metadata/<key>}` | From sandbox labels `security.agents.kruise.io/<key>` |
| `${ack:agent-identity/agent-name}` | From label `security.agents.kruise.io/agent-name` |

## AgenticBucket / BucketSpace (Upcoming)

See `docs/review/agentic-bucket-design.md` for the design to support BucketSpace-based isolation. Key changes:
- New volumeAttribute `agenticBucket` → ossfs mount option `agentic_bucket=<name>`
- `bucket` field reused for BucketSpace name
- ossfs auto-creates BucketSpace if not exists
- New policy template variables: `bucket-space-name`, `agentic-bucket-name` (names TBD)
