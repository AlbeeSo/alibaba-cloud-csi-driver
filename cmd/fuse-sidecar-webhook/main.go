package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/features"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/fusesidecar"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func main() {
	var address, configPath, cert, key, gates string
	flag.StringVar(&address, "listen", ":9443", "HTTPS listen address")
	flag.StringVar(&configPath, "config", "/etc/webhook/config.json", "operator sidecar profiles")
	flag.StringVar(&cert, "tls-cert", "/etc/tls/tls.crt", "TLS certificate")
	flag.StringVar(&key, "tls-key", "/etc/tls/tls.key", "TLS key")
	flag.StringVar(&gates, "feature-gates", "", "same feature gates as the CSI installation")
	flag.Parse()
	if err := features.FunctionalMutableFeatureGate.Set(gates); err != nil {
		log.Fatal(err)
	}
	b, err := os.ReadFile(configPath)
	if err != nil {
		log.Fatal(err)
	}
	var cfg fusesidecar.Config
	if err = json.Unmarshal(b, &cfg); err != nil {
		log.Fatal(err)
	}
	cfg.Defaults()
	if err = cfg.Validate(); err != nil {
		log.Fatal(err)
	}
	rc, err := rest.InClusterConfig()
	if err != nil {
		log.Fatal(err)
	}
	client, err := kubernetes.NewForConfig(rc)
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/mutate", fusesidecar.Handler{Client: client, Config: cfg})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	server := &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go func() {
		<-ctx.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	if err = server.ListenAndServeTLS(cert, key); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
