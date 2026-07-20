package customfuse

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"k8s.io/klog/v2"
)

const (
	credentialBaseDir      = "/var/run/customfuse/credentials"
	defaultRefreshMargin   = 5 * time.Minute
	maxRetryBackoff        = 30 * time.Second
	initialRetryBackoff    = 1 * time.Second
	httpTimeout            = 10 * time.Second
	apiActionGetCredential = "GetResourceCredential"
	credentialTypeSTSToken = "stsToken"
)

type AgentIdentityOpts struct {
	TokenFile    string
	Endpoint     string
	CredProvider string
	CAFile       string
}

type tokenFileContent struct {
	RequestID             string `json:"requestId"`
	AccessToken           string `json:"accessToken"`
	SandboxClientID       string `json:"sandboxClientId"`
	AccessTokenExpiration string `json:"accessTokenExpiration"`
}

type credentialRequest struct {
	CredentialType         string `json:"credentialType"`
	ResourceID             string `json:"resourceId"`
	CredentialProviderName string `json:"credentialProviderName"`
}

type stsToken struct {
	AccessKeyID     string `json:"accessKeyId"`
	AccessKeySecret string `json:"accessKeySecret"`
	SecurityToken   string `json:"securityToken"`
	Expiration      string `json:"expiration"`
}

type credentialResponse struct {
	RequestID string    `json:"requestId"`
	STSToken  *stsToken `json:"stsToken"`
}

type CredentialRefresher struct {
	tokenFile     string
	endpoint      string
	credProvider  string
	caFile        string
	outputDir     string
	refreshMargin time.Duration

	mu     sync.Mutex
	stopCh chan struct{}
	done   chan struct{}
	client *http.Client
}

func NewCredentialRefresher(opts AgentIdentityOpts, volumeID string) *CredentialRefresher {
	dir := filepath.Join(credentialBaseDir, volumeID)
	return &CredentialRefresher{
		tokenFile:     opts.TokenFile,
		endpoint:      opts.Endpoint,
		credProvider:  opts.CredProvider,
		caFile:        opts.CAFile,
		outputDir:     dir,
		refreshMargin: defaultRefreshMargin,
		stopCh:        make(chan struct{}),
		done:          make(chan struct{}),
	}
}

func (r *CredentialRefresher) Dir() string {
	return r.outputDir
}

func (r *CredentialRefresher) Start(ctx context.Context) error {
	if err := os.MkdirAll(r.outputDir, 0700); err != nil {
		return fmt.Errorf("create credential dir: %w", err)
	}

	r.client = r.buildHTTPClient()

	cred, err := r.fetchCredentials(ctx)
	if err != nil {
		return fmt.Errorf("initial credential fetch: %w", err)
	}
	if err := r.writeCredentials(cred); err != nil {
		return fmt.Errorf("write initial credentials: %w", err)
	}

	go r.refreshLoop(cred)
	return nil
}

func (r *CredentialRefresher) Stop() {
	r.mu.Lock()
	select {
	case <-r.stopCh:
	default:
		close(r.stopCh)
	}
	r.mu.Unlock()
	<-r.done
}

func (r *CredentialRefresher) buildHTTPClient() *http.Client {
	tlsConfig := &tls.Config{}

	if r.caFile != "" {
		caCert, err := os.ReadFile(r.caFile)
		if err == nil {
			pool := x509.NewCertPool()
			pool.AppendCertsFromPEM(caCert)
			tlsConfig.RootCAs = pool
		} else {
			klog.Warningf("CredentialRefresher: failed to read CA file %s, disabling TLS verify: %v", r.caFile, err)
			tlsConfig.InsecureSkipVerify = true
		}
	} else {
		tlsConfig.InsecureSkipVerify = true
	}

	return &http.Client{
		Timeout: httpTimeout,
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}
}

func (r *CredentialRefresher) readTokenFile() (*tokenFileContent, error) {
	data, err := os.ReadFile(r.tokenFile)
	if err != nil {
		return nil, fmt.Errorf("read token file %s: %w", r.tokenFile, err)
	}
	var token tokenFileContent
	if err := json.Unmarshal(data, &token); err != nil {
		return nil, fmt.Errorf("parse token file: %w", err)
	}
	if token.AccessToken == "" {
		return nil, fmt.Errorf("token file has empty accessToken")
	}
	if token.SandboxClientID == "" {
		return nil, fmt.Errorf("token file has empty sandboxClientId")
	}
	return &token, nil
}

func (r *CredentialRefresher) fetchCredentials(ctx context.Context) (*stsToken, error) {
	token, err := r.readTokenFile()
	if err != nil {
		return nil, err
	}

	reqBody := credentialRequest{
		CredentialType:         credentialTypeSTSToken,
		ResourceID:             token.SandboxClientID,
		CredentialProviderName: r.credProvider,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Action-Name", apiActionGetCredential)

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("credential request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("credential endpoint returned %d: %s", resp.StatusCode, string(respBody))
	}

	var credResp credentialResponse
	if err := json.Unmarshal(respBody, &credResp); err != nil {
		return nil, fmt.Errorf("parse credential response: %w", err)
	}
	if credResp.STSToken == nil {
		return nil, fmt.Errorf("credential response has nil stsToken")
	}
	if credResp.STSToken.AccessKeyID == "" || credResp.STSToken.AccessKeySecret == "" {
		return nil, fmt.Errorf("credential response has empty credentials")
	}

	return credResp.STSToken, nil
}

func (r *CredentialRefresher) writeCredentials(cred *stsToken) error {
	files := map[string]string{
		"AccessKeyId":     cred.AccessKeyID,
		"AccessKeySecret": cred.AccessKeySecret,
		"SecurityToken":   cred.SecurityToken,
		"Expiration":      cred.Expiration,
	}
	for name, value := range files {
		if err := atomicWriteFile(filepath.Join(r.outputDir, name), []byte(value)); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	return nil
}

func (r *CredentialRefresher) refreshLoop(lastCred *stsToken) {
	defer close(r.done)

	for {
		sleepDuration := r.calcSleepDuration(lastCred.Expiration)
		klog.V(4).Infof("CredentialRefresher: next refresh in %v", sleepDuration)

		select {
		case <-r.stopCh:
			return
		case <-time.After(sleepDuration):
		}

		cred, err := r.fetchWithRetry()
		if err != nil {
			klog.Errorf("CredentialRefresher: fetch failed after retries: %v", err)
			continue
		}

		if err := r.writeCredentials(cred); err != nil {
			klog.Errorf("CredentialRefresher: write credentials failed: %v", err)
			continue
		}

		lastCred = cred
		klog.V(2).Infof("CredentialRefresher: credentials refreshed, expires %s", cred.Expiration)
	}
}

func (r *CredentialRefresher) fetchWithRetry() (*stsToken, error) {
	backoff := initialRetryBackoff
	var lastErr error

	for i := 0; i < 5; i++ {
		select {
		case <-r.stopCh:
			return nil, fmt.Errorf("stopped")
		default:
		}

		ctx, cancel := context.WithTimeout(context.Background(), httpTimeout)
		cred, err := r.fetchCredentials(ctx)
		cancel()

		if err == nil {
			return cred, nil
		}
		lastErr = err
		klog.Warningf("CredentialRefresher: fetch attempt %d failed: %v", i+1, err)

		select {
		case <-r.stopCh:
			return nil, fmt.Errorf("stopped")
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > maxRetryBackoff {
			backoff = maxRetryBackoff
		}
	}
	return nil, fmt.Errorf("all retries exhausted: %w", lastErr)
}

func (r *CredentialRefresher) calcSleepDuration(expiration string) time.Duration {
	expTime, err := time.Parse(time.RFC3339, expiration)
	if err != nil {
		klog.Warningf("CredentialRefresher: parse expiration %q failed, using margin as sleep: %v", expiration, err)
		return r.refreshMargin
	}
	until := time.Until(expTime) - r.refreshMargin
	if until < 30*time.Second {
		return 30 * time.Second
	}
	return until
}

func atomicWriteFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
