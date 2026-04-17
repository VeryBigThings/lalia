package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

func TestSigningRegisterCreatesAndReusesKey(t *testing.T) {
	lescheHome := setupIntegrationEnv(t)
	defer stopDaemonForHome(t, lescheHome)

	first := mustRequest(t, "register", map[string]any{"name": "alice", "pid": float64(111)})
	if !first.OK {
		t.Fatalf("first register failed: %+v", first)
	}
	fdata := first.Data.(map[string]any)
	pub1, _ := fdata["pubkey"].(string)
	if pub1 == "" {
		t.Fatalf("register response missing pubkey: %+v", first)
	}
	pubBytes, err := hex.DecodeString(pub1)
	if err != nil || len(pubBytes) != ed25519.PublicKeySize {
		t.Fatalf("pubkey decode failed len=%d err=%v", len(pubBytes), err)
	}

	keyBytes1, err := os.ReadFile(keyPath("alice"))
	if err != nil {
		t.Fatalf("read key file after register: %v", err)
	}
	if len(keyBytes1) != ed25519.PrivateKeySize {
		t.Fatalf("private key size=%d, want %d", len(keyBytes1), ed25519.PrivateKeySize)
	}

	second := mustRequest(t, "register", map[string]any{"name": "alice", "pid": float64(222)})
	if !second.OK {
		t.Fatalf("second register failed: %+v", second)
	}
	pub2, _ := second.Data.(map[string]any)["pubkey"].(string)
	if pub1 != pub2 {
		t.Fatalf("pubkey changed across re-register: %q != %q", pub1, pub2)
	}

	keyBytes2, err := os.ReadFile(keyPath("alice"))
	if err != nil {
		t.Fatalf("read key file after re-register: %v", err)
	}
	if !bytes.Equal(keyBytes1, keyBytes2) {
		t.Fatalf("private key bytes changed across re-register")
	}
}

// TestSigningTellWithValidSignatureSucceeds verifies a signed tell round-trips
// and the peer can read it.
func TestSigningTellWithValidSignatureSucceeds(t *testing.T) {
	lescheHome := setupIntegrationEnv(t)
	defer stopDaemonForHome(t, lescheHome)

	mustRequest(t, "register", map[string]any{"name": "alice", "pid": float64(301)})
	mustRequest(t, "register", map[string]any{"name": "bob", "pid": float64(302)})

	tell := mustRequest(t, "tell", map[string]any{"from": "alice", "peer": "bob", "body": "signed hello"})
	if !tell.OK {
		t.Fatalf("signed tell failed: %+v", tell)
	}
	read := mustRequest(t, "read", map[string]any{"from": "bob", "peer": "alice", "timeout": float64(1)})
	if !read.OK {
		t.Fatalf("signed read failed: %+v", read)
	}
	if got := read.Data.(map[string]any)["body"].(string); got != "signed hello" {
		t.Fatalf("body=%q, want signed hello", got)
	}
}

// TestSigningTellMissingKeyFailsClientSide removes alice's key and verifies
// the client refuses to sign.
func TestSigningTellMissingKeyFailsClientSide(t *testing.T) {
	lescheHome := setupIntegrationEnv(t)
	defer stopDaemonForHome(t, lescheHome)

	mustRequest(t, "register", map[string]any{"name": "alice", "pid": float64(401)})
	mustRequest(t, "register", map[string]any{"name": "bob", "pid": float64(402)})

	if err := os.Remove(keyPath("alice")); err != nil {
		t.Fatalf("remove alice key: %v", err)
	}

	resp, err := request("tell", map[string]any{"from": "alice", "peer": "bob", "body": "won't send"})
	if err == nil {
		t.Fatalf("expected client-side key load error, got resp=%+v", resp)
	}
	if !strings.Contains(err.Error(), "load key for alice") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestSigningTellWrongKeyRejectedByServer swaps alice's key for a fresh
// unrelated pair; the daemon must reject the signature.
func TestSigningTellWrongKeyRejectedByServer(t *testing.T) {
	lescheHome := setupIntegrationEnv(t)
	defer stopDaemonForHome(t, lescheHome)

	mustRequest(t, "register", map[string]any{"name": "alice", "pid": float64(501)})
	mustRequest(t, "register", map[string]any{"name": "bob", "pid": float64(502)})

	_, badPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate bad private key: %v", err)
	}
	if err := os.WriteFile(keyPath("alice"), badPriv, 0600); err != nil {
		t.Fatalf("overwrite alice key: %v", err)
	}

	resp, err := request("tell", map[string]any{"from": "alice", "peer": "bob", "body": "forged"})
	if err != nil {
		t.Fatalf("unexpected client error on wrong-key tell: %v", err)
	}
	if resp.OK || resp.Code != CodeUnauthorized {
		t.Fatalf("wrong-key tell should be unauthorized: %+v", resp)
	}
	if !strings.Contains(resp.Error, "signature rejected") {
		t.Fatalf("expected signature rejected error, got: %q", resp.Error)
	}
}

func TestSigningNonRegisteredCallerUnauthorized(t *testing.T) {
	lescheHome := setupIntegrationEnv(t)
	defer stopDaemonForHome(t, lescheHome)

	mustRequest(t, "register", map[string]any{"name": "alice", "pid": float64(601)})

	if _, _, err := ensureKey("mallory"); err != nil {
		t.Fatalf("ensureKey for mallory: %v", err)
	}

	resp, err := request("renew", map[string]any{"from": "mallory"})
	if err != nil {
		t.Fatalf("unexpected client error for signed non-registered renew: %v", err)
	}
	if resp.OK || resp.Code != CodeUnauthorized {
		t.Fatalf("non-registered signed op should be unauthorized: %+v", resp)
	}
	if !strings.Contains(resp.Error, "not registered") {
		t.Fatalf("expected not registered error, got: %q", resp.Error)
	}
}
