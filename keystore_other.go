//go:build !darwin

package main

// newKeychainBackend returns nil on non-darwin platforms; the caller falls
// back to fileKeystore.
func newKeychainBackend() Keystore { return nil }
