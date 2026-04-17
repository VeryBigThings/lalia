package main

import (
	"crypto/ed25519"
	"fmt"
	"os"
)

// keychainService is the Keychain service name used for all kopos key items.
const keychainService = "kopos"

// Keystore abstracts private key storage. Two backends: file (default) and
// keychain (macOS Keychain via security CLI, enabled by KOPOS_KEYSTORE=keychain).
type Keystore interface {
	Load(name string) (ed25519.PrivateKey, error)
	Save(name string, key ed25519.PrivateKey) error
	// Delete removes the key for name. No-op if not present.
	Delete(name string) error
}

// newKeystore returns the active backend. If KOPOS_KEYSTORE=keychain and the
// keychain backend initialises without error, it is used; otherwise file.
func newKeystore() Keystore {
	if os.Getenv("KOPOS_KEYSTORE") == "keychain" {
		if ks := newKeychainBackend(); ks != nil {
			return ks
		}
	}
	return &fileKeystore{}
}

// fileKeystore stores keys as raw bytes at ~/.kopos/keys/<name>.key.
type fileKeystore struct{}

func (f *fileKeystore) Load(name string) (ed25519.PrivateKey, error) {
	p := keyPath(name)
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	if len(b) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("malformed key at %s: length %d", p, len(b))
	}
	return ed25519.PrivateKey(b), nil
}

func (f *fileKeystore) Save(name string, key ed25519.PrivateKey) error {
	if err := os.MkdirAll(keyDir(), 0700); err != nil {
		return err
	}
	return os.WriteFile(keyPath(name), key, 0600)
}

func (f *fileKeystore) Delete(name string) error {
	err := os.Remove(keyPath(name))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
