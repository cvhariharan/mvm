package sshutil

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

// GenerateKeyPair creates an ed25519 keypair in dir and returns the private
// key file path and the base64-encoded public key (suitable for kernel cmdline).
func GenerateKeyPair(dir string) (privPath, pubKeyBase64 string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate key: %w", err)
	}

	privPath = filepath.Join(dir, "ssh_key")
	privPEM, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return "", "", fmt.Errorf("marshal private key: %w", err)
	}
	if err := os.WriteFile(privPath, pem.EncodeToMemory(privPEM), 0600); err != nil {
		return "", "", fmt.Errorf("write private key: %w", err)
	}

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", "", fmt.Errorf("convert public key: %w", err)
	}
	pubBytes := ssh.MarshalAuthorizedKey(sshPub)
	pubPath := filepath.Join(dir, "ssh_key.pub")
	if err := os.WriteFile(pubPath, pubBytes, 0644); err != nil {
		return "", "", fmt.Errorf("write public key: %w", err)
	}

	pubKeyBase64 = base64.StdEncoding.EncodeToString(pubBytes)
	return privPath, pubKeyBase64, nil
}
