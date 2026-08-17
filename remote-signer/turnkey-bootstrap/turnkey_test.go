package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/keystore"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

func TestParseEthereumBIP44PathIndex(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		path     string
		expected int
		ok       bool
	}{
		{
			name:     "valid path",
			path:     "m/44'/60'/0'/0/7",
			expected: 7,
			ok:       true,
		},
		{
			name:     "invalid prefix",
			path:     "m/44'/60'/1'/0/0",
			expected: 0,
			ok:       false,
		},
		{
			name:     "invalid index",
			path:     "m/44'/60'/0'/0/x",
			expected: 0,
			ok:       false,
		},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseEthereumBIP44PathIndex(testCase.path)
			if ok != testCase.ok {
				t.Fatalf("ok mismatch: got %v want %v", ok, testCase.ok)
			}
			if got != testCase.expected {
				t.Fatalf("index mismatch: got %d want %d", got, testCase.expected)
			}
		})
	}
}

func TestParsePrivateKeyBytes(t *testing.T) {
	t.Parallel()

	const keyHex = "0x4c0883a69102937d6234146f454bcc8f2f4de2d855f6be63b97d93f57a7d4b77"

	raw32 := make([]byte, 32)
	for i := range raw32 {
		raw32[i] = byte(i + 1)
	}
	raw32[0] = 0xff
	raw32[10] = 0x00

	tests := []struct {
		name    string
		payload []byte
		ok      bool
		want    []byte
	}{
		{
			name:    "plain hex",
			payload: []byte(keyHex),
			ok:      true,
		},
		{
			name:    "json key",
			payload: []byte(`{"privateKey":"` + keyHex + `"}`),
			ok:      true,
		},
		{
			name:    "raw 32-byte key",
			payload: raw32,
			ok:      true,
			want:    raw32,
		},
		{
			name:    "invalid",
			payload: []byte("not-a-private-key"),
			ok:      false,
		},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got, err := parsePrivateKeyBytes(testCase.payload)
			if testCase.ok && err != nil {
				t.Fatalf("expected success, got error: %v", err)
			}
			if !testCase.ok && err == nil {
				t.Fatalf("expected error, got success (%x)", got)
			}
			if testCase.ok && len(got) != 32 {
				t.Fatalf("expected 32-byte key, got %d bytes", len(got))
			}
			if testCase.want != nil && !bytes.Equal(got, testCase.want) {
				t.Fatalf("raw key mismatch: got %x want %x", got, testCase.want)
			}
		})
	}
}

func TestWriteUTCKeystoreRoundTrip(t *testing.T) {
	t.Parallel()

	privateKey, err := ethcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	address := ethcrypto.PubkeyToAddress(privateKey.PublicKey)
	dir := t.TempDir()
	pwBytes := make([]byte, 16)
	if _, err := rand.Read(pwBytes); err != nil {
		t.Fatalf("generate password: %v", err)
	}
	password := hex.EncodeToString(pwBytes)

	if err := writeUTCKeystore(dir, privateKey, address, password); err != nil {
		t.Fatalf("write UTC keystore: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read keystore dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 keystore file, got %d", len(entries))
	}

	keyJSON, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read keystore file: %v", err)
	}

	unlocked, err := keystore.DecryptKey(keyJSON, password)
	if err != nil {
		t.Fatalf("decrypt with go-ethereum keystore: %v", err)
	}
	if unlocked.Address != address {
		t.Fatalf("address mismatch: got %s want %s", unlocked.Address.Hex(), address.Hex())
	}
	if hex.EncodeToString(ethcrypto.FromECDSA(unlocked.PrivateKey)) != hex.EncodeToString(ethcrypto.FromECDSA(privateKey)) {
		t.Fatal("round-tripped private key does not match")
	}
}
