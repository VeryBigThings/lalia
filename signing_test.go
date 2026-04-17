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

func TestSigningSendWithValidSignatureSucceeds(t *testing.T) {
	lescheHome := setupIntegrationEnv(t)
	defer stopDaemonForHome(t, lescheHome)

	mustRequest(t, "register", map[string]any{"name": "alice", "pid": float64(301)})
	mustRequest(t, "register", map[string]any{"name": "bob", "pid": float64(302)})

	open := mustRequest(t, "tunnel", map[string]any{"from": "alice", "peer": "bob"})
	if !open.OK {
		t.Fatalf("open tunnel failed: %+v", open)
	}
	sid := sidFrom(open)

	bobDone := make(chan requestResult, 1)
	go func() {
		first, err := request("await", map[string]any{"from": "bob", "sid": sid, "timeout": float64(3)})
		if err != nil {
			bobDone <- requestResult{err: err}
			return
		}
		if !first.OK {
			bobDone <- requestResult{resp: first}
			return
		}
		body, _ := first.Data.(map[string]any)["body"].(string)
		if body != "signed hello" {
			bobDone <- requestResult{resp: &Response{Error: "unexpected body: " + body}}
			return
		}
		r, err := request("send", map[string]any{"from": "bob", "sid": sid, "body": "signed ack", "timeout": float64(1)})
		bobDone <- requestResult{resp: r, err: err}
	}()

	aliceSend := mustRequest(t, "send", map[string]any{"from": "alice", "sid": sid, "body": "signed hello", "timeout": float64(3)})
	if !aliceSend.OK {
		t.Fatalf("alice signed send failed: %+v", aliceSend)
	}
	if got := aliceSend.Data.(map[string]any)["body"].(string); got != "signed ack" {
		t.Fatalf("alice send returned body=%q, want signed ack", got)
	}

	bobRes := <-bobDone
	if bobRes.err != nil {
		t.Fatalf("bob path error: %v", bobRes.err)
	}
	if bobRes.resp.OK || bobRes.resp.Code != CodeTimeout {
		t.Fatalf("bob send should timeout waiting for follow-up: %+v", bobRes.resp)
	}
}

func TestSigningSendMissingKeyFailsClientSide(t *testing.T) {
	lescheHome := setupIntegrationEnv(t)
	defer stopDaemonForHome(t, lescheHome)

	mustRequest(t, "register", map[string]any{"name": "alice", "pid": float64(401)})
	mustRequest(t, "register", map[string]any{"name": "bob", "pid": float64(402)})

	open := mustRequest(t, "tunnel", map[string]any{"from": "alice", "peer": "bob"})
	sid := sidFrom(open)

	if err := os.Remove(keyPath("alice")); err != nil {
		t.Fatalf("remove alice key: %v", err)
	}

	resp, err := request("send", map[string]any{"from": "alice", "sid": sid, "body": "won't send", "timeout": float64(1)})
	if err == nil {
		t.Fatalf("expected client-side key load error, got resp=%+v", resp)
	}
	if !strings.Contains(err.Error(), "load key for alice") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSigningSendWrongKeyRejectedByServer(t *testing.T) {
	lescheHome := setupIntegrationEnv(t)
	defer stopDaemonForHome(t, lescheHome)

	mustRequest(t, "register", map[string]any{"name": "alice", "pid": float64(501)})
	mustRequest(t, "register", map[string]any{"name": "bob", "pid": float64(502)})
	open := mustRequest(t, "tunnel", map[string]any{"from": "alice", "peer": "bob"})
	sid := sidFrom(open)

	_, badPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate bad private key: %v", err)
	}
	if err := os.WriteFile(keyPath("alice"), badPriv, 0600); err != nil {
		t.Fatalf("overwrite alice key: %v", err)
	}

	resp, err := request("send", map[string]any{"from": "alice", "sid": sid, "body": "forged", "timeout": float64(1)})
	if err != nil {
		t.Fatalf("unexpected client error on wrong-key send: %v", err)
	}
	if resp.OK || resp.Code != CodeUnauthorized {
		t.Fatalf("wrong-key send should be unauthorized: %+v", resp)
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
