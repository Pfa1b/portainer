package ecr

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"time"
)

const authorizationTokenTimeout = 30 * time.Second

func (s *Service) GetEncodedAuthorizationToken() (token *string, expiry *time.Time, err error) {
	return s.getEncodedAuthorizationToken(context.Background())
}

func (s *Service) getEncodedAuthorizationToken(ctx context.Context) (token *string, expiry *time.Time, err error) {
	ctx, cancel := context.WithTimeout(ctx, authorizationTokenTimeout)
	defer cancel()
	getAuthorizationTokenOutput, err := s.client.GetAuthorizationToken(ctx, nil)
	if err != nil {
		return
	}

	if len(getAuthorizationTokenOutput.AuthorizationData) == 0 {
		err = errors.New("AuthorizationData is empty")
		return
	}

	authData := getAuthorizationTokenOutput.AuthorizationData[0]
	if authData.AuthorizationToken == nil || authData.ExpiresAt == nil {
		return nil, nil, errors.New("incomplete ECR authorization data")
	}

	token = authData.AuthorizationToken
	expiry = authData.ExpiresAt

	return
}

func (s *Service) GetAuthorizationToken() (token *string, expiry *time.Time, err error) {
	return s.GetAuthorizationTokenWithContext(context.Background())
}

// GetAuthorizationTokenWithContext bounds the entire SDK operation, including retries.
func (s *Service) GetAuthorizationTokenWithContext(ctx context.Context) (token *string, expiry *time.Time, err error) {
	tokenEncodedStr, expiry, err := s.getEncodedAuthorizationToken(ctx)
	if err != nil {
		return
	}

	tokenByte, err := base64.StdEncoding.DecodeString(*tokenEncodedStr)
	if err != nil {
		return
	}
	tokenStr := string(tokenByte)
	token = &tokenStr

	return
}

func (s *Service) ParseAuthorizationToken(token string) (username string, password string, err error) {
	if len(token) == 0 {
		return
	}

	splitToken := strings.Split(token, ":")
	if len(splitToken) < 2 {
		err = errors.New("invalid ECR authorization token")
		return
	}

	username = splitToken[0]
	password = splitToken[1]

	return
}
