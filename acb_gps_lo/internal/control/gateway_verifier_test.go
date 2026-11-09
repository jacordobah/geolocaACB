package control

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"
)

func testGatewayToken(t *testing.T) string {
	t.Helper()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(secret)
}

func TestGatewayVerifierAndDelegatedScope(t *testing.T) {
	token := testGatewayToken(t)
	verifier, err := NewGatewayVerifier(token, true, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"", "wrong", testGatewayToken(t)} {
		if _, err := verifier.Verify(context.Background(), invalid); err == nil {
			t.Fatalf("accepted invalid Gateway credential")
		}
	}
	p, err := verifier.Verify(context.Background(), token)
	if err != nil || p.Subject != "acb-gateway" || p.Audience != Audience {
		t.Fatalf("verified principal: %+v %v", p, err)
	}
	for _, capability := range []string{CreateCapability, ReadCapability, FinishCapability} {
		if !p.Allows(time.Now(), capability, "authorized-by-gateway", "tracking-id") {
			t.Fatalf("Gateway capability %s rejected", capability)
		}
	}
	if p.Allows(time.Now(), "tracking:observe", "trip", "tracking") {
		t.Fatal("Gateway administrative credential granted observation capability")
	}
	ordinary := Principal{Subject: "ordinary", Audience: Audience, ExpiresAt: time.Now().Add(time.Hour), Capabilities: map[string]bool{CreateCapability: true}}
	if ordinary.Allows(time.Now(), CreateCapability, "unlisted-trip", "") {
		t.Fatal("ordinary principal bypassed trip scope")
	}
}

func TestGatewayVerifierRejectsBadConfiguration(t *testing.T) {
	for _, token := range []string{"", "short", base64.RawURLEncoding.EncodeToString(make([]byte, 31))} {
		if _, err := NewGatewayVerifier(token, true, false); err == nil {
			t.Fatal("invalid shared credential accepted")
		}
	}
	if _, err := NewGatewayVerifier(testGatewayToken(t), false, false); err == nil {
		t.Fatal("verifier without enabled interface accepted")
	}
}
