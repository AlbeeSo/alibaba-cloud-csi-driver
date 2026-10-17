package oss

import (
	"testing"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/utils/agentidentity"
	"github.com/stretchr/testify/assert"
)

func TestValidateAgentIdentity(t *testing.T) {
	tests := []struct {
		name     string
		opts     *Options
		setupEnv func(t *testing.T)
		wantErr  bool
		errMsg   string
	}{
		{
			name: "missing sandboxId",
			opts: &Options{
				SandboxCredProviderName: "aliyun-one",
			},
			wantErr: true,
			errMsg:  "missing sandboxId in volume attributes",
		},
		{
			name: "missing sandboxCredProviderName",
			opts: &Options{
				SandboxId: "sandbox-123",
			},
			wantErr: true,
			errMsg:  "missing sandboxCredProviderName in volume attributes",
		},
		{
			name: "AGENT_IDENTITY_ENDPOINT not set",
			opts: &Options{
				SandboxId:               "sandbox-123",
				SandboxCredProviderName: "aliyun-one",
			},
			wantErr: true,
			errMsg:  "AGENT_IDENTITY_ENDPOINT is not set",
		},
		{
			name: "AGENT_IDENTITY_TOKEN_DIR not set",
			opts: &Options{
				SandboxId:               "sandbox-123",
				SandboxCredProviderName: "aliyun-one",
			},
			setupEnv: func(t *testing.T) {
				t.Setenv("AGENT_IDENTITY_ENDPOINT", "https://endpoint.example.com")
			},
			wantErr: true,
			errMsg:  "AGENT_IDENTITY_TOKEN_DIR is not set",
		},
		{
			name: "success",
			opts: &Options{
				SandboxId:               "sandbox-123",
				SandboxCredProviderName: "aliyun-one",
			},
			setupEnv: func(t *testing.T) {
				t.Setenv("AGENT_IDENTITY_ENDPOINT", "https://endpoint.example.com")
				t.Setenv("AGENT_IDENTITY_TOKEN_DIR", "/var/opt/sandbox/agent-token")
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setupEnv != nil {
				tt.setupEnv(t)
			}
			err := ValidateAgentIdentity(tt.opts)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.EqualError(t, err, tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestMakeAgentIdentityConfig(t *testing.T) {
	opts := &Options{
		SandboxId:               "sandbox-123",
		SandboxCredProviderName: "aliyun-one",
	}

	cfg := MakeAgentIdentityConfig(opts)

	assert.NotNil(t, cfg)
	assert.Equal(t, "aliyun-one", cfg.CredProviderName)
	assert.Equal(t, "sandbox-123", cfg.SandboxId)
}

func TestGetAgentIdentityOptions(t *testing.T) {
	t.Setenv("AGENT_IDENTITY_ENDPOINT", "https://endpoint.example.com")
	t.Setenv("AGENT_IDENTITY_TOKEN_DIR", "/var/opt/sandbox/agent-token")

	opts := &Options{
		SandboxId:               "sandbox-123",
		SandboxCredProviderName: "aliyun-one",
	}

	mountOpts := GetAgentIdentityOptions(opts)

	expected := []string{
		"agent_identity_endpoint=https://endpoint.example.com",
		"agent_identity_token_file=/var/opt/sandbox/agent-token/sandbox-123.token",
		"agent_identity_cred_provider=aliyun-one",
	}

	assert.Equal(t, expected, mountOpts)
}

func TestGetAgentIdentityOptions_DifferentSandboxId(t *testing.T) {
	t.Setenv("AGENT_IDENTITY_ENDPOINT", "https://custom-endpoint:8443")
	t.Setenv("AGENT_IDENTITY_TOKEN_DIR", "/custom/token/dir")

	opts := &Options{
		SandboxId:               "sandbox-abc-456",
		SandboxCredProviderName: "oss-readonly",
	}

	mountOpts := GetAgentIdentityOptions(opts)

	assert.Len(t, mountOpts, 3)
	assert.Contains(t, mountOpts[0], "agent_identity_endpoint=https://custom-endpoint:8443")
	assert.Contains(t, mountOpts[1], "agent_identity_token_file=/custom/token/dir/sandbox-abc-456.token")
	assert.Contains(t, mountOpts[2], "agent_identity_cred_provider=oss-readonly")
}

// Substrate mode does not read the ACS token dir, so validation must not
// require AGENT_IDENTITY_TOKEN_DIR; it still requires the endpoint and the
// Actor-scoped sandbox identity.
func TestValidateAgentIdentity_Substrate(t *testing.T) {
	t.Run("token dir not required", func(t *testing.T) {
		t.Setenv("AGENT_IDENTITY_ENDPOINT", "https://endpoint.example.com")
		err := ValidateAgentIdentity(&Options{
			SandboxId:               "actor-uid-123",
			SandboxCredProviderName: "aliyun-one",
			SubstrateMode:           true,
		})
		assert.NoError(t, err)
	})
	t.Run("endpoint still required", func(t *testing.T) {
		err := ValidateAgentIdentity(&Options{
			SandboxId:               "actor-uid-123",
			SandboxCredProviderName: "aliyun-one",
			SubstrateMode:           true,
		})
		assert.EqualError(t, err, "AGENT_IDENTITY_ENDPOINT is not set")
	})
	t.Run("sandbox identity still required", func(t *testing.T) {
		t.Setenv("AGENT_IDENTITY_ENDPOINT", "https://endpoint.example.com")
		err := ValidateAgentIdentity(&Options{SandboxCredProviderName: "aliyun-one", SubstrateMode: true})
		assert.EqualError(t, err, "missing sandboxId in volume attributes")
	})
}

// Substrate mode hands ossfs the substrate_* options: the Actor UID as the
// resource, the projected ServiceAccount token as the bearer, and the combined
// PodCertificate bundle for both the client cert and key. No ACS token file.
func TestGetAgentIdentityOptions_Substrate(t *testing.T) {
	t.Setenv("AGENT_IDENTITY_ENDPOINT", "https://endpoint.example.com")
	t.Setenv("AGENT_IDENTITY_TOKEN_DIR", "/var/opt/sandbox/agent-token")

	mountOpts := GetAgentIdentityOptions(&Options{
		SandboxId:               "actor-uid-123",
		SandboxCredProviderName: "aliyun-one",
		SubstrateMode:           true,
	})

	expected := []string{
		"agent_identity_endpoint=https://endpoint.example.com",
		"agent_identity_cred_provider=aliyun-one",
		"substrate_actor_uid=actor-uid-123",
		"substrate_sa_token_file=" + agentidentity.SATokenFile,
		"substrate_client_cert=" + agentidentity.PodCertBundleFile,
		"substrate_client_key=" + agentidentity.PodCertBundleFile,
	}
	assert.Equal(t, expected, mountOpts)
	assert.NotContains(t, mountOpts, "agent_identity_token_file=/var/opt/sandbox/agent-token/actor-uid-123.token")
}
