package stacks

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
func fakeECR(t *testing.T) {
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
	t.Setenv("NO_PROXY", "")
}

type ecrLabDeployer struct{ libstack.Deployer }

func (ecrLabDeployer) Deploy(ctx context.Context, paths []string, opts libstack.DeployOptions) error {
	return nil
}

// Run alone in a fresh process: Go caches system certificate roots and proxy env.
// Before the fix this test times out with UpdateTx -> ECR refresh -> beginRWTx.
func TestStackUpdateECRTransaction(t *testing.T) {
	fakeECR(t)
	fips.InitFIPS(false)
	s := setupUpdateStackInTxTest(t, &portainer.Stack{ID: 1, Name: "ecr-lab", EntryPoint: "compose.yml", Type: portainer.DockerComposeStack}, &updateComposeStackPayload{StackFileContent: "services:\n  web:\n    image: alpine:3.20\n", Env: []portainer.Pair{{Name: "LAB_REVISION", Value: "new"}}})
	s.endpoint.URL = "unix:///var/run/docker.sock"
	require.NoError(t, s.store.Endpoint().UpdateEndpoint(s.endpoint.ID, s.endpoint))
	reg := &portainer.Registry{Type: portainer.EcrRegistry, Name: "lab", URL: "lab.invalid", Authentication: true, Username: "lab-key", Password: "lab-secret", Ecr: portainer.EcrData{Region: "us-east-1"}, AccessTokenExpiry: 1}
	require.NoError(t, s.store.Registry().Create(reg))
	manager := stackexec.NewComposeStackManager(ecrLabDeployer{}, nil, s.store)
	s.handler.ComposeStackManager = manager
	s.handler.StackDeployer = deployments.NewStackDeployer(nil, manager, nil, nil, s.store)
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, s.req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got, err := s.store.Stack().Read(s.stack.ID)
	require.NoError(t, err)
	require.Equal(t, "new", got.Env[0].Value)
	saved, err := s.store.Registry().Read(reg.ID)
	require.NoError(t, err)
	require.Greater(t, saved.AccessTokenExpiry, time.Now().Unix())
	require.NoError(t, s.store.Registry().Update(saved.ID, saved))
	t.Log("METADATA_COMMITTED=YES WRITER_RELEASED=YES")
}
