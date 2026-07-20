package customfuse

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeTokenFile(t *testing.T, dir, accessToken, sandboxClientID string) string {
	t.Helper()
	tokenPath := filepath.Join(dir, "token.json")
	content := tokenFileContent{
		RequestID:             "req-1",
		AccessToken:           accessToken,
		SandboxClientID:       sandboxClientID,
		AccessTokenExpiration: time.Now().Add(time.Hour).Format(time.RFC3339),
	}
	data, err := json.Marshal(content)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(tokenPath, data, 0600))
	return tokenPath
}

func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestCredentialRefresher_SuccessfulFetchAndWrite(t *testing.T) {
	tmpDir := t.TempDir()
	tokenPath := writeTokenFile(t, tmpDir, "test-token", "client-123")

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "GetResourceCredential", r.Header.Get("X-Api-Action-Name"))

		var req credentialRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "stsToken", req.CredentialType)
		assert.Equal(t, "client-123", req.ResourceID)
		assert.Equal(t, "my-provider", req.CredentialProviderName)

		resp := credentialResponse{
			RequestID: "resp-1",
			STSToken: &stsToken{
				AccessKeyID:     "AKID-test",
				AccessKeySecret: "AKSECRET-test",
				SecurityToken:   "TOKEN-test",
				Expiration:      time.Now().Add(time.Hour).Format(time.RFC3339),
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	outputDir := filepath.Join(tmpDir, "creds")
	refresher := &CredentialRefresher{
		tokenFile:     tokenPath,
		endpoint:      srv.URL,
		credProvider:  "my-provider",
		outputDir:     outputDir,
		refreshMargin: defaultRefreshMargin,
		stopCh:        make(chan struct{}),
		done:          make(chan struct{}),
	}

	err := refresher.Start(context.Background())
	require.NoError(t, err)
	defer refresher.Stop()

	assert.Equal(t, outputDir, refresher.Dir())

	akID, err := os.ReadFile(filepath.Join(outputDir, "AccessKeyId"))
	require.NoError(t, err)
	assert.Equal(t, "AKID-test", string(akID))

	akSecret, err := os.ReadFile(filepath.Join(outputDir, "AccessKeySecret"))
	require.NoError(t, err)
	assert.Equal(t, "AKSECRET-test", string(akSecret))

	secToken, err := os.ReadFile(filepath.Join(outputDir, "SecurityToken"))
	require.NoError(t, err)
	assert.Equal(t, "TOKEN-test", string(secToken))

	expiration, err := os.ReadFile(filepath.Join(outputDir, "Expiration"))
	require.NoError(t, err)
	assert.NotEmpty(t, string(expiration))
}

func TestCredentialRefresher_TokenFileErrors(t *testing.T) {
	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "creds")

	tests := []struct {
		name      string
		tokenData string
		wantErr   string
	}{
		{
			name:      "missing file",
			tokenData: "",
			wantErr:   "read token file",
		},
		{
			name:      "invalid json",
			tokenData: "not json",
			wantErr:   "parse token file",
		},
		{
			name:      "empty accessToken",
			tokenData: `{"accessToken":"","sandboxClientId":"c1"}`,
			wantErr:   "empty accessToken",
		},
		{
			name:      "empty sandboxClientId",
			tokenData: `{"accessToken":"tok","sandboxClientId":""}`,
			wantErr:   "empty sandboxClientId",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokenPath := filepath.Join(tmpDir, "token-"+tt.name+".json")
			if tt.tokenData != "" {
				require.NoError(t, os.WriteFile(tokenPath, []byte(tt.tokenData), 0600))
			}

			refresher := &CredentialRefresher{
				tokenFile:     tokenPath,
				endpoint:      "http://localhost:0",
				credProvider:  "prov",
				outputDir:     outputDir,
				refreshMargin: defaultRefreshMargin,
				stopCh:        make(chan struct{}),
				done:          make(chan struct{}),
			}

			err := refresher.Start(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestCredentialRefresher_EndpointErrors(t *testing.T) {
	tmpDir := t.TempDir()
	tokenPath := writeTokenFile(t, tmpDir, "tok", "cli")

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	})

	outputDir := filepath.Join(tmpDir, "creds")
	refresher := &CredentialRefresher{
		tokenFile:     tokenPath,
		endpoint:      srv.URL,
		credProvider:  "prov",
		outputDir:     outputDir,
		refreshMargin: defaultRefreshMargin,
		stopCh:        make(chan struct{}),
		done:          make(chan struct{}),
	}

	err := refresher.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

func TestCredentialRefresher_NilSTSToken(t *testing.T) {
	tmpDir := t.TempDir()
	tokenPath := writeTokenFile(t, tmpDir, "tok", "cli")

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(credentialResponse{RequestID: "r1", STSToken: nil})
	})

	outputDir := filepath.Join(tmpDir, "creds")
	refresher := &CredentialRefresher{
		tokenFile:     tokenPath,
		endpoint:      srv.URL,
		credProvider:  "prov",
		outputDir:     outputDir,
		refreshMargin: defaultRefreshMargin,
		stopCh:        make(chan struct{}),
		done:          make(chan struct{}),
	}

	err := refresher.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil stsToken")
}

func TestCredentialRefresher_StopDuringRefresh(t *testing.T) {
	tmpDir := t.TempDir()
	tokenPath := writeTokenFile(t, tmpDir, "tok", "cli")

	var callCount atomic.Int32
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		resp := credentialResponse{
			RequestID: "r1",
			STSToken: &stsToken{
				AccessKeyID:     "ak",
				AccessKeySecret: "sk",
				SecurityToken:   "st",
				Expiration:      time.Now().Add(2 * time.Second).Format(time.RFC3339),
			},
		}
		json.NewEncoder(w).Encode(resp)
	})

	outputDir := filepath.Join(tmpDir, "creds")
	refresher := &CredentialRefresher{
		tokenFile:     tokenPath,
		endpoint:      srv.URL,
		credProvider:  "prov",
		outputDir:     outputDir,
		refreshMargin: 1 * time.Second,
		stopCh:        make(chan struct{}),
		done:          make(chan struct{}),
	}

	err := refresher.Start(context.Background())
	require.NoError(t, err)

	assert.Equal(t, int32(1), callCount.Load())

	refresher.Stop()

	// Verify Stop() actually stops — no more calls after stop
	time.Sleep(100 * time.Millisecond)
	finalCount := callCount.Load()
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, finalCount, callCount.Load())
}

func TestCredentialRefresher_CalcSleepDuration(t *testing.T) {
	r := &CredentialRefresher{refreshMargin: 5 * time.Minute}

	t.Run("normal expiration", func(t *testing.T) {
		exp := time.Now().Add(30 * time.Minute).Format(time.RFC3339)
		d := r.calcSleepDuration(exp)
		assert.InDelta(t, 25*time.Minute, d, float64(5*time.Second))
	})

	t.Run("near expiration clamps to 30s", func(t *testing.T) {
		exp := time.Now().Add(1 * time.Minute).Format(time.RFC3339)
		d := r.calcSleepDuration(exp)
		assert.Equal(t, 30*time.Second, d)
	})

	t.Run("past expiration clamps to 30s", func(t *testing.T) {
		exp := time.Now().Add(-10 * time.Minute).Format(time.RFC3339)
		d := r.calcSleepDuration(exp)
		assert.Equal(t, 30*time.Second, d)
	})

	t.Run("invalid format uses margin", func(t *testing.T) {
		d := r.calcSleepDuration("not-a-date")
		assert.Equal(t, 5*time.Minute, d)
	})
}

func TestAtomicWriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	err := atomicWriteFile(path, []byte("hello"))
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(data))

	// Verify tmp file is cleaned up
	_, err = os.Stat(path + ".tmp")
	assert.True(t, os.IsNotExist(err))
}
