/*
Copyright 2026 The Kubernetes Authors.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package substrate

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/substrate/internal/actorpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

//go:generate protoc --go_out=. --go_opt=paths=source_relative internal/actorpb/actor.proto

type ActorClientOptions struct {
	Endpoint   string
	CAFile     string
	TokenFile  string
	ServerName string
}

type ActorClient struct{ conn *grpc.ClientConn }

type ActorInfo struct {
	UID         string
	Atespace    string
	Name        string
	Annotation  string
	Golden      bool
	TemplateUID string
}

func NewActorClient(opts ActorClientOptions) (*ActorClient, error) {
	if opts.Endpoint == "" || opts.CAFile == "" || opts.TokenFile == "" {
		return nil, fmt.Errorf("Substrate endpoint, CA and projected token file are required")
	}
	ca, err := os.ReadFile(opts.CAFile)
	if err != nil {
		return nil, fmt.Errorf("read Substrate CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("Substrate CA contains no certificates")
	}
	conn, err := grpc.NewClient(opts.Endpoint,
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: opts.ServerName})),
		grpc.WithPerRPCCredentials(fileToken(opts.TokenFile)),
	)
	if err != nil {
		return nil, fmt.Errorf("create Substrate connection: %w", err)
	}
	return &ActorClient{conn: conn}, nil
}

func (c *ActorClient) Lookup(ctx context.Context, uid string) (ActorInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	response := new(actorpb.Actor)
	err := c.conn.Invoke(ctx, "/ateapi.Control/GetActorByUID", &actorpb.GetActorByUIDRequest{ActorUid: uid}, response)
	if err != nil {
		return ActorInfo{}, err
	}
	if response.GetMetadata().GetUid() != uid {
		return ActorInfo{}, status.Error(codes.PermissionDenied, "actor lookup returned a different UID")
	}
	meta := response.GetMetadata()
	if meta.GetAtespace() == "" || meta.GetName() == "" {
		return ActorInfo{}, status.Error(codes.FailedPrecondition, "actor metadata lacks namespace or name")
	}
	info := ActorInfo{UID: uid, Atespace: meta.Atespace, Name: meta.Name, Annotation: meta.GetAnnotations()[PublishRequestsAnnotation]}
	if info.Atespace != "ate-golden" {
		return info, nil
	}
	ref := response.GetActorTemplate()
	if ref.GetAtespace() == "" || ref.GetName() == "" {
		return ActorInfo{}, status.Error(codes.FailedPrecondition, "golden actor lacks a template reference")
	}
	template := new(actorpb.ActorTemplate)
	if err := c.conn.Invoke(ctx, "/ateapi.Control/GetActorTemplate", &actorpb.GetActorTemplateRequest{ActorTemplate: ref}, template); err != nil {
		return ActorInfo{}, err
	}
	templateMeta := template.GetMetadata()
	if templateMeta.GetAtespace() != ref.Atespace || templateMeta.GetName() != ref.Name || templateMeta.GetUid() == "" || templateMeta.GetUid() != info.Name {
		return ActorInfo{}, status.Error(codes.FailedPrecondition, "golden actor does not match its template UID")
	}
	info.Golden = true
	info.TemplateUID = templateMeta.Uid
	return info, nil
}

func (c *ActorClient) Close() error { return c.conn.Close() }

type fileToken string

func (f fileToken) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	raw, err := os.ReadFile(string(f))
	if err != nil {
		return nil, fmt.Errorf("read projected Substrate token: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return nil, fmt.Errorf("projected Substrate token is empty")
	}
	return map[string]string{"authorization": "Bearer " + token}, nil
}

func (fileToken) RequireTransportSecurity() bool { return true }
