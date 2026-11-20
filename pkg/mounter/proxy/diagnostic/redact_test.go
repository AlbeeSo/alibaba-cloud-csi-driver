package diagnostic

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestGeneratedAndKnownCredentialsKeepFailureContext(t *testing.T) {
	sts := "STS." + strings.Repeat("x", 24)
	jwtValue := "eyJ" + strings.Repeat("a", 12) + "." + strings.Repeat("b", 16) + "." + strings.Repeat("c", 16)
	secret := "unit secret with spaces"
	input := `NoSuchBucket with ak ` + sts + ` error -2; {"AccessKeySecret":"` + secret + `"}; OIDCToken=` + jwtValue + `&next=1; <SecurityToken>xml-private</SecurityToken> https://unit-user:unit-password@example.invalid/path opaque-known`
	got := Redact(input, map[string]string{"opaque": "opaque-known"})
	for _, value := range []string{sts, jwtValue, secret, "xml-private", "unit-password", "opaque-known"} {
		if strings.Contains(got, value) {
			t.Fatalf("credential remained in diagnostic: %q", value)
		}
	}
	for _, context := range []string{"NoSuchBucket", "error -2", "next=1", "example.invalid/path"} {
		if !strings.Contains(got, context) {
			t.Fatalf("lost failure context %q: %s", context, got)
		}
	}
}

func TestWriterSplitCredentialsAndRouting(t *testing.T) {
	value := "STS." + strings.Repeat("x", 24)
	line := "client NoSuchBucket with ak " + value + "\n"
	for split := 1; split < len(line); split++ {
		var initial, final bytes.Buffer
		w := NewWriter(&initial, nil)
		if n, err := w.Write([]byte(line[:split])); err != nil || n != split {
			t.Fatalf("write: %d %v", n, err)
		}
		if initial.Len() != 0 {
			t.Fatal("partial line published before credential classification")
		}
		w.SwitchTarget(&final)
		if _, err := w.Write([]byte(line[split:])); err != nil {
			t.Fatal(err)
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
		if initial.Len() != 0 || strings.Contains(final.String(), value) || !strings.Contains(final.String(), "NoSuchBucket") {
			t.Fatalf("unsafe or missing diagnostic at split %d", split)
		}
	}
}

func TestWriterCapsUnterminatedLine(t *testing.T) {
	var output bytes.Buffer
	w := NewWriter(&output, nil)
	input := strings.Repeat("x", maxLine*3) + "\nnext diagnostic\n"
	n, err := w.Write([]byte(input))
	if err != nil || n != len(input) {
		t.Fatalf("write: %d %v", n, err)
	}
	if !strings.Contains(output.String(), "oversized client diagnostic omitted") || !strings.Contains(output.String(), "next diagnostic") || len(w.pending) > maxLine {
		t.Fatal("unbounded or missing diagnostics")
	}
}

func TestProcessDiagnosticsWithoutFinalNewline(t *testing.T) {
	var stdout, stderr bytes.Buffer
	secret := "unit-private-value"
	cmd := exec.Command("sh", "-c", `printf '%s' "$TEST_PRIVATE"; printf 'AccessDenied SecurityToken=%s' "$TEST_PRIVATE" >&2; exit 7`)
	cmd.Env = []string{"TEST_PRIVATE=" + secret}
	cmd.Stdout = NewWriter(&stdout, map[string]string{"private": secret})
	cmd.Stderr = NewWriter(&stderr, map[string]string{"private": secret})
	if cmd.Run() == nil {
		t.Fatal("expected child failure")
	}
	FlushCommand(cmd)
	if strings.Contains(stdout.String()+stderr.String(), secret) || !strings.Contains(stderr.String(), "AccessDenied") || stdout.Len() == 0 {
		t.Fatal("lost or unredacted EOF diagnostic")
	}
}

type blockingWriter struct{ entered, release chan struct{} }

func (w blockingWriter) Write(p []byte) (int, error) {
	close(w.entered)
	<-w.release
	return len(p), nil
}
func TestRoutingChangeDoesNotWaitForBlockedLogSink(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	writer := NewWriter(blockingWriter{entered: entered, release: release}, nil)
	done := make(chan struct{})
	go func() { _, _ = writer.Write([]byte("old line\n")); close(done) }()
	<-entered
	var final bytes.Buffer
	switched := make(chan struct{})
	go func() { writer.SwitchTarget(&final); close(switched) }()
	select {
	case <-switched:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("routing waits for log IO")
	}
	close(release)
	<-done
	_, err := writer.Write([]byte("new line\n"))
	if err != nil {
		t.Fatal(err)
	}
	if final.String() != "new line\n" {
		t.Fatal("lost new routing")
	}
}

func TestSplitArgumentCredentialsAndAuthorization(t *testing.T) {
	args := []string{"--oss_access_key_secret", "unit-private", "--region=cn-test", "--token=opaque-private"}
	got := strings.Join(Args(args, nil), " ")
	if strings.Contains(got, "unit-private") || strings.Contains(got, "opaque-private") || !strings.Contains(got, "cn-test") {
		t.Fatal("unsafe argument diagnostics")
	}
	got = Redact("Authorization: Bearer opaque-private, NoSuchBucket", nil)
	if strings.Contains(got, "opaque-private") || !strings.Contains(got, "NoSuchBucket") {
		t.Fatal("unsafe authorization diagnostic")
	}
}
