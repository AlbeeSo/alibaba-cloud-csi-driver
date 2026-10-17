package oss

import (
	"fmt"

	fpm "github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/fuse_pod_manager"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/utils/agentidentity"
)

// ValidateAgentIdentity validates the agent identity auth configuration.
// Returns error if required fields are missing or environment variables are not set.
func ValidateAgentIdentity(o *Options) error {
	if o.SandboxId == "" {
		return fmt.Errorf("missing sandboxId in volume attributes")
	}
	if o.SandboxCredProviderName == "" {
		return fmt.Errorf("missing sandboxCredProviderName in volume attributes")
	}
	if agentidentity.GetEndpoint() == "" {
		return fmt.Errorf("AGENT_IDENTITY_ENDPOINT is not set")
	}
	if o.SubstrateMode {
		// Substrate restricted delegation exchanges credentials inside the ossfs
		// client, presenting the projected ServiceAccount token and PodCertificate
		// bundle at fixed paths in the sandbox. It never reads the ACS token dir,
		// so AGENT_IDENTITY_TOKEN_DIR is not required here; the client validates
		// the projected files at mount time, where they are actually visible.
		return nil
	}
	if agentidentity.GetTokenDir() == "" {
		return fmt.Errorf("AGENT_IDENTITY_TOKEN_DIR is not set")
	}
	return nil
}

// MakeAgentIdentityConfig creates an AgentIdentityConfig from the options.
func MakeAgentIdentityConfig(o *Options) *fpm.AgentIdentityConfig {
	return &fpm.AgentIdentityConfig{
		CredProviderName: o.SandboxCredProviderName,
		SandboxId:        o.SandboxId,
	}
}

// GetAgentIdentityOptions returns the mount options for agent identity auth.
func GetAgentIdentityOptions(o *Options) []string {
	// Env vars are validated in ValidateAgentIdentity.
	if o.SubstrateMode {
		// Substrate restricted delegation: the ossfs client does the credential
		// exchange itself. It presents the Actor UID as the resource, the projected
		// ServiceAccount token as the bearer, and the PodCertificate bundle for
		// mTLS. The cert and key are the same combined bundle: both ossfs1
		// (libcurl) and ossfs2 (photon) read the certificate and the private key
		// out of that one PEM, and the client re-reads it on every renewal so
		// PodCertificate and SA-token rotation are picked up without a remount.
		return []string{
			fmt.Sprintf("agent_identity_endpoint=%s", agentidentity.GetEndpoint()),
			fmt.Sprintf("agent_identity_cred_provider=%s", o.SandboxCredProviderName),
			fmt.Sprintf("substrate_actor_uid=%s", o.SandboxId),
			fmt.Sprintf("substrate_sa_token_file=%s", agentidentity.SATokenFile),
			fmt.Sprintf("substrate_client_cert=%s", agentidentity.PodCertBundleFile),
			fmt.Sprintf("substrate_client_key=%s", agentidentity.PodCertBundleFile),
		}
	}
	return []string{
		fmt.Sprintf("agent_identity_endpoint=%s", agentidentity.GetEndpoint()),
		fmt.Sprintf("agent_identity_token_file=%s", agentidentity.GetTokenFilePath(o.SandboxId)),
		fmt.Sprintf("agent_identity_cred_provider=%s", o.SandboxCredProviderName),
		// agent_identity_ca_file is not added here — it is optional and only appended
		// by ApplyOptionDefaults if the file is readable. See AgentIdentityConfig for details.
	}
}
