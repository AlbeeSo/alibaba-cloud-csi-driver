package fusesidecarwebhook

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	admission "k8s.io/api/admission/v1"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func (in Injector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var review admission.AdmissionReview
	if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&review); err != nil || review.Request == nil {
		http.Error(w, "invalid AdmissionReview", http.StatusBadRequest)
		return
	}
	req := review.Request
	response := &admission.AdmissionResponse{UID: req.UID, Allowed: true}
	deny := func(err error) {
		response.Allowed = false
		response.Result = &metav1.Status{Message: err.Error(), Reason: metav1.StatusReasonInvalid, Code: 422}
	}
	if req.Operation == admission.Create && req.Resource.Group == "" && req.Resource.Resource == "pods" && req.SubResource == "" {
		var pod core.Pod
		if err := json.Unmarshal(req.Object.Raw, &pod); err != nil {
			deny(fmt.Errorf("invalid Pod"))
		} else {
			pod.Namespace = req.Namespace
			ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
			mutated, err := in.Mutate(ctx, &pod)
			cancel()
			if err != nil {
				deny(err)
			} else if mutated != &pod {
				patch, err := json.Marshal([]map[string]any{{"op": "add", "path": "/spec/initContainers", "value": mutated.Spec.InitContainers}, {"op": "add", "path": "/spec/volumes", "value": mutated.Spec.Volumes}})
				if err != nil {
					deny(fmt.Errorf("encode sidecar patch"))
				} else {
					kind := admission.PatchTypeJSONPatch
					response.PatchType, response.Patch = &kind, patch
				}
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(admission.AdmissionReview{TypeMeta: metav1.TypeMeta{APIVersion: "admission.k8s.io/v1", Kind: "AdmissionReview"}, Response: response})
}

func Run(ctx context.Context, address, certFile, keyFile string, in Injector) error {
	config, err := rest.InClusterConfig()
	if err != nil {
		return err
	}
	config.Timeout = 4 * time.Second
	in.Client, err = kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}
	// Read the projected files again at each handshake, so Secret rotation does
	// not require a restart and never pins an old certificate inode.
	getCertificate := func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		return &cert, err
	}
	if _, err := getCertificate(nil); err != nil {
		return fmt.Errorf("load webhook certificate: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/mutate", in)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	server := &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 6 * time.Second, WriteTimeout: 6 * time.Second, IdleTimeout: 30 * time.Second, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: getCertificate}}
	go func() {
		<-ctx.Done()
		timeout, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(timeout)
	}()
	err = server.ListenAndServeTLS("", "")
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
