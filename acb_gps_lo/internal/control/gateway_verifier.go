package control

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"time"
)

// GatewayVerifier authenticates one private, shared Gateway service credential.
// It does not identify or authorize the original administrative user. That
// decision must be enforced by Gateway before forwarding a request to GPS.
type GatewayVerifier struct {
	digest       [32]byte
	capabilities map[string]bool
}

func NewGatewayVerifier(encodedToken string, controlEnabled, readsEnabled bool) (*GatewayVerifier, error) {
	secret, err := base64.RawURLEncoding.DecodeString(encodedToken)
	if err != nil || len(secret) < 32 || len(secret) > 64 || base64.RawURLEncoding.EncodeToString(secret) != encodedToken || (!controlEnabled && !readsEnabled) {
		return nil, ErrUnauthenticated
	}
	caps := map[string]bool{}
	if controlEnabled {
		caps[CreateCapability] = true
		caps[FinishCapability] = true
		caps[ReadCapability] = true // GET session status is part of control.
	}
	if readsEnabled {
		caps[ReadCapability] = true
	}
	return &GatewayVerifier{digest: sha256.Sum256(secret), capabilities: caps}, nil
}

func (v *GatewayVerifier) Verify(ctx context.Context, token string) (Principal, error) {
	if v == nil || ctx.Err() != nil {
		return Principal{}, ErrUnauthenticated
	}
	provided, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(provided) < 32 || len(provided) > 64 || base64.RawURLEncoding.EncodeToString(provided) != token {
		return Principal{}, ErrUnauthenticated
	}
	digest := sha256.Sum256(provided)
	if subtle.ConstantTimeCompare(digest[:], v.digest[:]) != 1 {
		return Principal{}, ErrUnauthenticated
	}
	now := time.Now()
	return Principal{Subject: "acb-gateway", Audience: Audience, NotBefore: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute), Capabilities: v.capabilities, gatewayTrusted: true}, nil
}
