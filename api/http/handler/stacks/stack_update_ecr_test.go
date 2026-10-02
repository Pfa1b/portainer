package stacks

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/portainer/portainer/pkg/libstack/compose"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	portainer "github.com/portainer/portainer/api"
	stackexec "github.com/portainer/portainer/api/exec"
	"github.com/portainer/portainer/api/stacks/deployments"
	"github.com/portainer/portainer/pkg/fips"
	"github.com/portainer/portainer/pkg/libstack"
	"github.com/stretchr/testify/require"
)

// The real SDK connects through a loopback CONNECT proxy to this TLS ECR stub.
// No production credentials, endpoints, Docker socket or AWS network are used.
func fakeECR(t *testing.T) (*atomic.Bool, *atomic.Int32) {
	var fail atomic.Bool
	var calls atomic.Int32
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "lab-ecr"}, DNSNames: []string{"api.ecr.us-east-1.amazonaws.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	require.NoError(t, err)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600))
	t.Setenv("SSL_CERT_FILE", ca)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if fail.Load() {
			w.Header().Set("Content-Type", "application/x-amz-json-1.1")
			w.WriteHeader(400)
			fmt.Fprint(w, `{"__type":"UnrecognizedClientException","message":"lab failure"}`)
			return
		}
		t.Log("AWS_SIMULATED_SUCCESS")
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		fmt.Fprintf(w, `{"authorizationData":[{"authorizationToken":%q,"expiresAt":%d}]}`, base64.StdEncoding.EncodeToString([]byte("AWS:lab-token")), time.Now().Add(12*time.Hour).Unix())
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect || r.Host != "api.ecr.us-east-1.amazonaws.com:443" {
			http.Error(w, "unexpected destination", 403)
			return
		}
		dst, e := net.Dial("tcp", srv.Listener.Addr().String())
		if e != nil {
			http.Error(w, "stub unavailable", 502)
			return
		}
		src, _, e := w.(http.Hijacker).Hijack()
		if e != nil {
			dst.Close()
			return
		}
		fmt.Fprint(src, "HTTP/1.1 200 Connection Established\r\n\r\n")
		go func() { defer src.Close(); defer dst.Close(); io.Copy(dst, src) }()
		io.Copy(src, dst)
	}))
	t.Cleanup(proxy.Close)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "astra-portainer-dind,localhost,127.0.0.1")
	return &fail, &calls
}

type ecrLabDeployer struct {
	libstack.Deployer
	fail   bool
	called *atomic.Int32
}

func (d ecrLabDeployer) Deploy(ctx context.Context, paths []string, opts libstack.DeployOptions) error {
	d.called.Add(1)
	if d.fail {
		return errors.New("lab compose failure")
	}
	return nil
}
func (d ecrLabDeployer) Pull(ctx context.Context, paths []string, opts libstack.Options) error {
	return nil
}

// TLS trust and proxy environment are process-global in Go: use one fixture and
// serial subtests. The two-stack control runs concurrent requests inside one case.
func TestStackUpdateECRTransaction(t *testing.T) {
	failAWS, awsCalls := fakeECR(t)
	fips.InitFIPS(false)
	for _, tc := range []struct {
		name                                                        string
		valid, nonECR, awsError, composeError, forcePull, twoStacks bool
	}{
		{name: "expired"}, {name: "valid", valid: true}, {name: "non-ecr", nonECR: true},
		{name: "aws-error", awsError: true}, {name: "compose-error", composeError: true},
		{name: "force-pull", forcePull: true}, {name: "concurrent-two-stacks", twoStacks: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failAWS.Store(tc.awsError)
			beforeCalls := awsCalls.Load()
			payload := &updateComposeStackPayload{StackFileContent: "services:\n  web:\n    image: alpine:3.20\n    pull_policy: never\n    command: [sleep, '3600']\n", Env: []portainer.Pair{{Name: "LAB_REVISION", Value: "new"}}, PullImage: tc.forcePull}
			s := setupUpdateStackInTxTest(t, &portainer.Stack{ID: 1, Name: "ecr-lab", EntryPoint: "compose.yml", Type: portainer.DockerComposeStack}, payload)
			s.endpoint.URL = "unix:///var/run/docker.sock"
			require.NoError(t, s.store.Endpoint().UpdateEndpoint(s.endpoint.ID, s.endpoint))
			reg := &portainer.Registry{Type: portainer.EcrRegistry, Name: "lab", URL: "lab.invalid", Authentication: true, Username: "lab-key", Password: "lab-secret", Ecr: portainer.EcrData{Region: "us-east-1"}, AccessToken: "AWS:expired-lab-token", AccessTokenExpiry: 1}
			if tc.valid {
				reg.AccessToken = "AWS:existing-lab-token"
				reg.AccessTokenExpiry = time.Now().Add(time.Hour).Unix()
			}
			if tc.nonECR {
				reg.Type = portainer.CustomRegistry
			}
			require.NoError(t, s.store.Registry().Create(reg))
			var called atomic.Int32
			var plugin libstack.Deployer = ecrLabDeployer{fail: tc.composeError, called: &called}
			if os.Getenv("ASTRA_PORTAINER_E2E") == "1" {
				require.Equal(t, "expired", tc.name, "E2E runs only the expired case")
				plugin = compose.NewComposeDeployer()
			}
			manager := stackexec.NewComposeStackManager(plugin, nil, s.store)
			s.handler.ComposeStackManager = manager
			s.handler.StackDeployer = deployments.NewStackDeployer(nil, manager, nil, nil, s.store)
			requests := []*http.Request{s.req}
			if tc.twoStacks {
				second := *s.stack
				second.ID = 2
				second.Name = "ecr-lab-two"
				second.ProjectPath = filepath.Join(s.fileService.GetDatastorePath(), "compose", "2")
				require.NoError(t, s.store.Stack().Create(&second))
				_, err := s.fileService.StoreStackFileFromBytes("2", second.EntryPoint, []byte("services:\n  web:\n    image: alpine:3.20\n"))
				require.NoError(t, err)
				body, err := json.Marshal(payload)
				require.NoError(t, err)
				requests = append(requests, mockCreateStackRequestWithSecurityContext(http.MethodPut, "/stacks/2?endpointId=1", bytes.NewReader(body)))
			}
			completed := make(chan *httptest.ResponseRecorder, len(requests))
			start := time.Now()
			for _, req := range requests {
				go func(req *http.Request) {
					rec := httptest.NewRecorder()
					s.handler.ServeHTTP(rec, req)
					completed <- rec
				}(req)
			}
			for range requests {
				select {
				case rec := <-completed:
					expected := http.StatusOK
					if tc.composeError {
						expected = http.StatusInternalServerError
					}
					require.Equal(t, expected, rec.Code)
					t.Logf("PUT_HTTP=%d PUT_DURATION=%s", rec.Code, time.Since(start))
				case <-time.After(5 * time.Second):
					t.Fatal("stack update did not finish")
				}
			}
			got, err := s.store.Stack().Read(s.stack.ID)
			require.NoError(t, err)
			saved, err := s.store.Registry().Read(reg.ID)
			require.NoError(t, err)
			content, err := os.ReadFile(filepath.Join(s.stack.ProjectPath, s.stack.EntryPoint))
			require.NoError(t, err)
			if tc.composeError {
				require.Empty(t, got.Env)
				require.Equal(t, int64(1), saved.AccessTokenExpiry)
				require.Contains(t, string(content), "nginx:v1")
				t.Log("METADATA_ROLLBACK=YES REGISTRY_ROLLBACK=YES COMPOSE_ROLLBACK=YES")
			} else {
				require.Equal(t, "new", got.Env[0].Value)
				require.Equal(t, payload.StackFileContent, string(content))
				if !tc.awsError && !tc.nonECR {
					require.Greater(t, saved.AccessTokenExpiry, time.Now().Unix())
				}
				if tc.twoStacks {
					second, err := s.store.Stack().Read(2)
					require.NoError(t, err)
					require.Equal(t, "new", second.Env[0].Value)
				}
				t.Log("METADATA_COMMITTED=YES COMPOSE_UPDATED=YES")
			}
			if tc.valid || tc.nonECR {
				require.Equal(t, beforeCalls, awsCalls.Load())
			} else {
				require.Greater(t, awsCalls.Load(), beforeCalls)
			}
			require.NoError(t, s.store.Registry().Update(saved.ID, saved))
			t.Log("WRITER_RELEASED=YES NO_DEADLOCK=YES")
		})
	}
}
