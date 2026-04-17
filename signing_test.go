package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
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

// TestKeystoreFileRoundTrip verifies that the file backend saves and reloads
// a private key without corruption.
func TestKeystoreFileRoundTrip(t *testing.T) {
	base := t.TempDir()
	t.Setenv("LESCHE_HOME", base)
	t.Setenv("LESCHE_KEYSTORE", "")

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	ks := &fileKeystore{}
	if err := ks.Save("roundtrip", priv); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := ks.Load("roundtrip")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !bytes.Equal([]byte(priv), []byte(loaded)) {
		t.Fatalf("loaded key differs from saved key")
	}
}

// TestKeystoreKeychainRoundTrip tests the macOS Keychain backend.
// Skipped when the 'security' CLI is unavailable (Linux CI, non-macOS).
func TestKeystoreKeychainRoundTrip(t *testing.T) {
	if newKeychainBackend() == nil {
		t.Skip("keychain backend unavailable on this platform")
	}
	name := "lesche-keystore-test-roundtrip"
	t.Cleanup(func() {
		exec.Command("security", "delete-generic-password", "-s", keychainService, "-a", name).Run() //nolint
	})

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	ks := newKeychainBackend()
	if err := ks.Save(name, priv); err != nil {
		t.Fatalf("keychain save: %v", err)
	}
	loaded, err := ks.Load(name)
	if err != nil {
		t.Fatalf("keychain load: %v", err)
	}
	if !bytes.Equal([]byte(priv), []byte(loaded)) {
		t.Fatalf("loaded key differs from saved key")
	}
}

// TestKeystoreKeychainUpdate verifies that saving over an existing keychain
// item replaces it (the -U flag to security add-generic-password).
func TestKeystoreKeychainUpdate(t *testing.T) {
	if newKeychainBackend() == nil {
		t.Skip("keychain backend unavailable on this platform")
	}
	name := "lesche-keystore-test-update"
	t.Cleanup(func() {
		exec.Command("security", "delete-generic-password", "-s", keychainService, "-a", name).Run() //nolint
	})

	_, priv1, _ := ed25519.GenerateKey(rand.Reader)
	_, priv2, _ := ed25519.GenerateKey(rand.Reader)
	ks := newKeychainBackend()

	if err := ks.Save(name, priv1); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := ks.Save(name, priv2); err != nil {
		t.Fatalf("second save: %v", err)
	}
	loaded, err := ks.Load(name)
	if err != nil {
		t.Fatalf("load after update: %v", err)
	}
	if !bytes.Equal([]byte(priv2), []byte(loaded)) {
		t.Fatalf("expected updated key, got stale one")
	}
}

// TestKeystoreKeychainFallbackToFile verifies that when LESCHE_KEYSTORE=keychain
// but the keychain backend is unavailable, newKeystore() returns a fileKeystore.
// This test only runs on platforms where the keychain backend is absent.
func TestKeystoreKeychainFallbackToFile(t *testing.T) {
	if newKeychainBackend() != nil {
		t.Skip("keychain backend available; fallback path not exercised here")
	}
	base := t.TempDir()
	t.Setenv("LESCHE_HOME", base)
	t.Setenv("LESCHE_KEYSTORE", "keychain")

	ks := newKeystore()
	if _, ok := ks.(*fileKeystore); !ok {
		t.Fatalf("expected file backend fallback, got %T", ks)
	}
}

// TestKeystoreEnsureKeyUsesKeychain confirms that ensureKey round-trips
// through the keychain backend when LESCHE_KEYSTORE=keychain.
func TestKeystoreEnsureKeyUsesKeychain(t *testing.T) {
	if newKeychainBackend() == nil {
		t.Skip("keychain backend unavailable on this platform")
	}
	name := "lesche-keystore-test-ensurekey"
	t.Cleanup(func() {
		exec.Command("security", "delete-generic-password", "-s", keychainService, "-a", name).Run() //nolint
	})
	t.Setenv("LESCHE_KEYSTORE", "keychain")

	pub1, priv1, err := ensureKey(name)
	if err != nil {
		t.Fatalf("first ensureKey: %v", err)
	}
	pub2, priv2, err := ensureKey(name)
	if err != nil {
		t.Fatalf("second ensureKey: %v", err)
	}
	if hex.EncodeToString(pub1) != hex.EncodeToString(pub2) {
		t.Fatalf("pubkey changed across re-ensureKey via keychain")
	}
	if !bytes.Equal([]byte(priv1), []byte(priv2)) {
		t.Fatalf("privkey changed across re-ensureKey via keychain")
	}
}

// TestKeystoreFileDelete verifies that Delete removes the key file and that
// a subsequent Load returns an error.
func TestKeystoreFileDelete(t *testing.T) {
	base := t.TempDir()
	t.Setenv("LESCHE_HOME", base)
	t.Setenv("LESCHE_KEYSTORE", "")

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	ks := &fileKeystore{}
	if err := ks.Save("todelete", priv); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := ks.Delete("todelete"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := ks.Load("todelete"); err == nil {
		t.Fatal("expected error loading deleted key, got nil")
	}
	// Second delete of missing key must be a no-op.
	if err := ks.Delete("todelete"); err != nil {
		t.Fatalf("second delete (no-op) returned error: %v", err)
	}
}

// TestKeystoreKeychainDelete verifies that the keychain backend deletes an
// item and that a subsequent Load returns an error.
func TestKeystoreKeychainDelete(t *testing.T) {
	if newKeychainBackend() == nil {
		t.Skip("keychain backend unavailable on this platform")
	}
	name := "lesche-keystore-test-delete"
	t.Cleanup(func() {
		exec.Command("security", "delete-generic-password", "-s", keychainService, "-a", name).Run() //nolint
	})

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	ks := newKeychainBackend()
	if err := ks.Save(name, priv); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := ks.Delete(name); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := ks.Load(name); err == nil {
		t.Fatal("expected error loading deleted keychain item, got nil")
	}
	// Second delete of missing item must be a no-op.
	if err := ks.Delete(name); err != nil {
		t.Fatalf("second delete (no-op) returned error: %v", err)
	}
}
