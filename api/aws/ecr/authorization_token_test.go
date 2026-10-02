package ecr

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	sdk "github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/stretchr/testify/require"
)

func TestAuthorizationTokenDeadline(t *testing.T) {
	for _, mode := range []string{"success", "aws-error", "incomplete", "caller-timeout", "cancel", "no-response"} {
		t.Run(mode, func(t *testing.T) {
			started := make(chan struct{}, 1)
			release := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				started <- struct{}{}
				switch mode {
				case "aws-error":
					w.WriteHeader(400)
					fmt.Fprint(w, `{"__type":"UnrecognizedClientException","message":"lab error"}`)
				case "incomplete":
					fmt.Fprint(w, `{"authorizationData":[{}]}`)
				case "caller-timeout", "cancel", "no-response":
					select {
					case <-r.Context().Done():
					case <-release:
					}
				default:
					fmt.Fprintf(w, `{"authorizationData":[{"authorizationToken":%q,"expiresAt":%d}]}`, base64.StdEncoding.EncodeToString([]byte("AWS:fake")), time.Now().Add(time.Hour).Unix())
				}
			}))
			defer srv.Close()
			defer close(release)
			service := &Service{client: sdk.New(sdk.Options{Region: "us-east-1", BaseEndpoint: aws.String(srv.URL), Credentials: credentials.NewStaticCredentialsProvider("fake", "fake", ""), RetryMaxAttempts: 1})}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "caller-timeout" {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 100*time.Millisecond)
				defer stop()
			}
			if mode == "cancel" {
				go func() { <-started; cancel() }()
			}
			begin := time.Now()
			token, expiry, err := service.GetAuthorizationTokenWithContext(ctx)
			elapsed := time.Since(begin)
			switch mode {
			case "success":
				require.NoError(t, err)
				require.Equal(t, "AWS:fake", *token)
				require.True(t, expiry.After(time.Now()))
			case "caller-timeout":
				require.ErrorIs(t, err, context.DeadlineExceeded)
				require.Less(t, elapsed, 2*time.Second)
			case "cancel":
				require.ErrorIs(t, err, context.Canceled)
				require.Less(t, elapsed, 2*time.Second)
			case "no-response":
				require.True(t, errors.Is(err, context.DeadlineExceeded))
				require.GreaterOrEqual(t, elapsed, authorizationTokenTimeout)
				require.Less(t, elapsed, authorizationTokenTimeout+3*time.Second)
			default:
				require.Error(t, err)
			}
			t.Logf("AWS_MODE=%s DURATION=%s", mode, elapsed)
		})
	}
}
