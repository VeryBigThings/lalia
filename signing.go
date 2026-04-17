package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// keyDir is where per-agent private keys live. Deliberately outside the
// workspace so they are not committed to git and can be placed outside
// the agent-harness deny list (agents must be able to read their own
// key to sign).
func keyDir() string {
	return filepath.Join(leschDir(), "keys")
}

func keyPath(name string) string {
	return filepath.Join(keyDir(), name+".key")
}

// ensureKey generates an Ed25519 keypair for `name` if one does not
// already exist on disk. Returns (pub, priv) as raw bytes.
// If a key already exists, it is loaded and returned unchanged.
func ensureKey(name string) (ed25519.PublicKey, ed25519.PrivateKey, error) {
	p := keyPath(name)
	if b, err := os.ReadFile(p); err == nil {
		if len(b) != ed25519.PrivateKeySize {
			return nil, nil, fmt.Errorf("key %s malformed: length %d", p, len(b))
		}
		priv := ed25519.PrivateKey(b)
		pub := priv.Public().(ed25519.PublicKey)
		return pub, priv, nil
	} else if !os.IsNotExist(err) {
		return nil, nil, err
	}
	if err := os.MkdirAll(keyDir(), 0700); err != nil {
		return nil, nil, err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(p, priv, 0600); err != nil {
		return nil, nil, err
	}
	return pub, priv, nil
}

// loadPrivateKey loads an agent's private key from disk. Used by the
// client side to sign outgoing requests.
func loadPrivateKey(name string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(keyPath(name))
	if err != nil {
		return nil, err
	}
	if len(b) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("malformed key at %s", keyPath(name))
	}
	return ed25519.PrivateKey(b), nil
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
