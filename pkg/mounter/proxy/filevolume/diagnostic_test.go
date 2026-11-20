package filevolume

import (
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
	"strings"
	"testing"
)

func TestRedactGeneratedRRSACredentialsBeforeTruncation(t *testing.T) {
	id := "STS." + strings.Repeat("x", 24)
	token := "eyJ" + strings.Repeat("a", 12) + "." + strings.Repeat("b", 5000) + "." + strings.Repeat("c", 16)
	message := redact("NoSuchBucket with ak "+id+" OIDCToken="+token, nil)
	if strings.Contains(message, id) || strings.Contains(message, "eyJ") || !strings.Contains(message, "NoSuchBucket") {
		t.Fatal("generated RRSA diagnostic leaked or lost its cause")
	}
}

func TestKnownSecretIsNotPartiallyExposedBySourceMask(t *testing.T) {
	desired := &Desired{Mount: proxy.MountRequest{Source: "source-prefix", Secrets: map[string]string{"secret": "source-prefix-private-value"}}}
	got := redact("source-prefix failed: source-prefix-private-value", desired)
	if strings.Contains(got, "private-value") {
		t.Fatal("source replacement split a known secret")
	}
}
