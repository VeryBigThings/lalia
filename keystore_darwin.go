//go:build darwin

package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"
)

// keychainKeystore delegates to the macOS 'security' CLI.  Keys are stored
// as generic passwords: service=lesche, account=<agent name>, password=<hex>.
type keychainKeystore struct{}

// newKeychainBackend returns a keychainKeystore if the 'security' binary is
// available, nil otherwise (caller falls back to fileKeystore).
func newKeychainBackend() Keystore {
	if _, err := exec.LookPath("security"); err != nil {
		return nil
	}
	return &keychainKeystore{}
}

func (k *keychainKeystore) Load(name string) (ed25519.PrivateKey, error) {
	out, err := exec.Command(
		"security", "find-generic-password",
		"-s", keychainService, "-a", name, "-w",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("keychain load %s: %w", name, err)
	}
	b, err := hex.DecodeString(strings.TrimSpace(string(out)))
	if err != nil {
		return nil, fmt.Errorf("keychain decode %s: %w", name, err)
	}
	if len(b) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("keychain key %s malformed: size %d", name, len(b))
	}
	return ed25519.PrivateKey(b), nil
}

func (k *keychainKeystore) Save(name string, key ed25519.PrivateKey) error {
	cmd := exec.Command(
		"security", "add-generic-password",
		"-s", keychainService, "-a", name,
		"-w", hex.EncodeToString(key),
		"-U", // update if exists
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("keychain save %s: %w — %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (k *keychainKeystore) Delete(name string) error {
	cmd := exec.Command(
		"security", "delete-generic-password",
		"-s", keychainService, "-a", name,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		// exit 44 means "item not found" — treat as no-op
		if strings.Contains(string(out), "could not be found") || strings.Contains(string(out), "44") {
			return nil
		}
		return fmt.Errorf("keychain delete %s: %w — %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}
