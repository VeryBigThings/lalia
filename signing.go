package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
)

// keyDir is where per-agent private keys live when using the file backend.
// Deliberately outside the workspace so keys are not committed to git.
func keyDir() string {
	return filepath.Join(leschDir(), "keys")
}

func keyPath(name string) string {
	return filepath.Join(keyDir(), name+".key")
}

// ensureKey returns an existing key or generates a new one, using the active
// keystore backend (file by default; keychain when LALIA_KEYSTORE=keychain).
func ensureKey(name string) (ed25519.PublicKey, ed25519.PrivateKey, error) {
	ks := newKeystore()
	priv, err := ks.Load(name)
	if err == nil {
		return priv.Public().(ed25519.PublicKey), priv, nil
	}
	// Key not found — generate a new one.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	if err := ks.Save(name, priv); err != nil {
		return nil, nil, fmt.Errorf("save key for %s: %w", name, err)
	}
	return pub, priv, nil
}

// removeKey deletes an agent's private key via the active keystore. No-op if missing.
func removeKey(name string) error {
	return newKeystore().Delete(name)
}

// loadPrivateKey loads an agent's private key via the active keystore.
// Used by the client side to sign outgoing requests.
func loadPrivateKey(name string) (ed25519.PrivateKey, error) {
	return newKeystore().Load(name)
}

// canonicalArgs returns a deterministic JSON encoding of a request's
// args with the "sig" field removed. Signatures sign this byte string.
// Map keys are sorted lexicographically to make the encoding stable
// across Go versions and client/daemon boundaries.
func canonicalArgs(args map[string]any) ([]byte, error) {
	if args == nil {
		return []byte("null"), nil
	}
	stripped := make(map[string]any, len(args))
	for k, v := range args {
		if k == "sig" {
			continue
		}
		stripped[k] = v
	}
	keys := make([]string, 0, len(stripped))
	for k := range stripped {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	// build a slice of [k, v] pairs so encoding is independent of
	// Go map iteration order.
	pairs := make([]any, 0, len(keys)*2)
	for _, k := range keys {
		pairs = append(pairs, k, stripped[k])
	}
	return json.Marshal(pairs)
}

// signRequest signs the canonical form of args with priv and returns
// the signature as a hex string.
func signRequest(priv ed25519.PrivateKey, args map[string]any) (string, error) {
	body, err := canonicalArgs(args)
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(priv, body)
	return hex.EncodeToString(sig), nil
}

// verifyRequest verifies a request's signature against the registered
// pubkey. Returns nil on valid, error on invalid.
func verifyRequest(pubHex string, args map[string]any) error {
	sigHex, ok := args["sig"].(string)
	if !ok || sigHex == "" {
		return fmt.Errorf("missing signature")
	}
	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		return fmt.Errorf("bad signature encoding: %w", err)
	}
	pub, err := hex.DecodeString(pubHex)
	if err != nil {
		return fmt.Errorf("bad pubkey encoding: %w", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("pubkey size %d != %d", len(pub), ed25519.PublicKeySize)
	}
	body, err := canonicalArgs(args)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, body, sig) {
		return fmt.Errorf("signature invalid")
	}
	return nil
}
