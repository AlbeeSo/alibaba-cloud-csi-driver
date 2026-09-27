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
	"crypto/tls"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/substrate/internal/actorpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestActorLookupTLSAndTokenRotation(t *testing.T) {
	seed := httptest.NewTLSServer(http.NotFoundHandler())
	certificate := seed.TLS.Certificates[0]
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: seed.Certificate().Raw})
	seed.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	tokens := make(chan string, 2)
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})), grpc.UnknownServiceHandler(func(_ interface{}, stream grpc.ServerStream) error {
		method, _ := grpc.MethodFromServerStream(stream)
		if method != "/ateapi.Control/GetActorByUID" {
			return status.Error(codes.Unimplemented, "unexpected method")
		}
		request := new(actorpb.GetActorByUIDRequest)
		if err := stream.RecvMsg(request); err != nil {
			return err
		}
		md, _ := metadata.FromIncomingContext(stream.Context())
		values := md.Get("authorization")
		if len(values) != 1 {
			return status.Error(codes.Unauthenticated, "missing bearer")
		}
		tokens <- values[0]
		return stream.SendMsg(&actorpb.Actor{Metadata: &actorpb.ResourceMetadata{Uid: request.ActorUid, Atespace: "storage-test", Name: "actor", Annotations: map[string]string{PublishRequestsAnnotation: "[]"}}})
	}))
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); require.NoError(t, <-done) })
	dir := t.TempDir()
	caPath, tokenPath := filepath.Join(dir, "ca.pem"), filepath.Join(dir, "token")
	require.NoError(t, os.WriteFile(caPath, ca, 0600))
	require.NoError(t, os.WriteFile(tokenPath, []byte("first"), 0600))
	client, err := NewActorClient(ActorClientOptions{Endpoint: listener.Addr().String(), CAFile: caPath, TokenFile: tokenPath})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	for _, token := range []string{"first", "rotated"} {
		require.NoError(t, os.WriteFile(tokenPath, []byte(token+"\n"), 0600))
		got, err := client.Lookup(t.Context(), testUID)
		require.NoError(t, err)
		require.Equal(t, "[]", got.Annotation)
		require.False(t, got.Golden)
		require.Equal(t, "Bearer "+token, <-tokens)
	}
}

func TestActorLookupVerifiesGoldenTemplate(t *testing.T) {
	for _, tc := range []struct {
		name        string
		actorName   string
		templateUID string
		golden      bool
		fail        bool
	}{
		{"verified", "template-uid", "template-uid", true, false},
		{"mismatched template", "another-template", "template-uid", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			server := grpc.NewServer(grpc.UnknownServiceHandler(func(_ interface{}, stream grpc.ServerStream) error {
				method, _ := grpc.MethodFromServerStream(stream)
				switch method {
				case "/ateapi.Control/GetActorByUID":
					request := new(actorpb.GetActorByUIDRequest)
					if err := stream.RecvMsg(request); err != nil {
						return err
					}
					return stream.SendMsg(&actorpb.Actor{Metadata: &actorpb.ResourceMetadata{Uid: request.ActorUid, Atespace: "ate-golden", Name: tc.actorName}, ActorTemplate: &actorpb.ObjectRef{Atespace: "storage-test", Name: "template"}})
				case "/ateapi.Control/GetActorTemplate":
					request := new(actorpb.GetActorTemplateRequest)
					if err := stream.RecvMsg(request); err != nil {
						return err
					}
					return stream.SendMsg(&actorpb.ActorTemplate{Metadata: &actorpb.ResourceMetadata{Uid: tc.templateUID, Atespace: request.ActorTemplate.Atespace, Name: request.ActorTemplate.Name}})
				default:
					return status.Error(codes.Unimplemented, "unexpected method")
				}
			}))
			done := make(chan error, 1)
			go func() { done <- server.Serve(listener) }()
			t.Cleanup(func() { server.Stop(); require.NoError(t, <-done) })
			conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, conn.Close()) })
			info, err := (&ActorClient{conn: conn}).Lookup(t.Context(), testUID)
			if tc.fail {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.golden, info.Golden)
			require.Equal(t, tc.templateUID, info.TemplateUID)
		})
	}
}

func TestActorClientRejectsMissingTrust(t *testing.T) {
	_, err := NewActorClient(ActorClientOptions{Endpoint: "localhost:443"})
	require.Error(t, err)
}
