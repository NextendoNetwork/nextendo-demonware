package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// CTR's bdAuth verifies RSA-PSS/SHA-256 with a zero-length salt. Go's
// SignPSS treats SaltLength=0 as automatic, so use OpenSSL's explicit zero
// setting. The matching public key must be installed in the CTR client mod.
type ctrAuthSigner struct {
	path    string
	openssl string
	public  *rsa.PublicKey
}

func loadCTRAuthSigner(path string) (*ctrAuthSigner, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("CTR signing key: %w", err)
	}
	p, _ := pem.Decode(b)
	if p == nil {
		return nil, fmt.Errorf("CTR signing key is not PEM")
	}
	var key *rsa.PrivateKey
	if p.Type == "RSA PRIVATE KEY" {
		key, err = x509.ParsePKCS1PrivateKey(p.Bytes)
	} else {
		var parsed any
		parsed, err = x509.ParsePKCS8PrivateKey(p.Bytes)
		if err == nil {
			key, _ = parsed.(*rsa.PrivateKey)
		}
	}
	if err != nil || key == nil {
		return nil, fmt.Errorf("CTR signing key must be an RSA private key")
	}
	if key.N.BitLen() != 2048 || key.E != 65537 {
		return nil, fmt.Errorf("CTR signing key must be RSA-2048 with exponent 65537")
	}
	if err := key.Validate(); err != nil {
		return nil, fmt.Errorf("invalid CTR signing key: %w", err)
	}
	bin, err := exec.LookPath("openssl")
	if err != nil {
		return nil, fmt.Errorf("CTR signing requires openssl: %w", err)
	}
	s := &ctrAuthSigner{path, bin, &key.PublicKey}
	// Fail startup if the configured key/tool cannot produce a valid signature.
	if _, err := s.sign([]byte("CTR signing startup self-test")); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *ctrAuthSigner) sign(body []byte) (string, error) {
	digest := sha256.Sum256(body)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.openssl, "pkeyutl", "-sign", "-inkey", s.path,
		"-pkeyopt", "digest:sha256", "-pkeyopt", "rsa_padding_mode:pss", "-pkeyopt", "rsa_pss_saltlen:0")
	cmd.Stdin = bytes.NewReader(digest[:])
	sig, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("CTR auth signing failed: %w", err)
	}
	if len(sig) != 256 {
		return "", fmt.Errorf("CTR auth signature has unexpected length")
	}
	if err := rsa.VerifyPSS(s.public, crypto.SHA256, digest[:], sig, nil); err != nil {
		return "", fmt.Errorf("CTR auth signature self-check failed: %w", err)
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}
