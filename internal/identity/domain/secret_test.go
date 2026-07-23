package domain_test

import (
	"testing"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

const testSecretPrefix = "test_"

func TestGenerate_HasPrefixAndExpectedLength(t *testing.T) {
	secret, err := domain.Generate(testSecretPrefix)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(secret) != len(testSecretPrefix)+64 {
		t.Errorf("len(secret) = %d, want %d (prefix + 64 hex chars for 32 bytes)", len(secret), len(testSecretPrefix)+64)
	}
	if secret[:len(testSecretPrefix)] != testSecretPrefix {
		t.Errorf("Generate(%q) = %q, want it to start with the prefix", testSecretPrefix, secret)
	}
}

func TestGenerate_UniqueAcrossManyDraws(t *testing.T) {
	seen := make(map[string]bool)
	const draws = 1000
	for range draws {
		secret, err := domain.Generate(testSecretPrefix)
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if seen[secret] {
			t.Fatalf("Generate produced a duplicate secret across %d draws", draws)
		}
		seen[secret] = true
	}
}

func TestHash_NeverEqualsThePlaintext(t *testing.T) {
	secret, err := domain.Generate(testSecretPrefix)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if hash := domain.Hash(secret); hash == secret {
		t.Error("Hash(secret) == secret, want the digest to differ from the plaintext")
	}
}

func TestHash_IsStable(t *testing.T) {
	const raw = "test_deadbeef"
	first := domain.Hash(raw)
	second := domain.Hash(raw)
	if first != second {
		t.Errorf("Hash(%q) = %q then %q, want the same digest both times", raw, first, second)
	}
}

func TestSecretsMatch(t *testing.T) {
	secret, err := domain.Generate(testSecretPrefix)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	hash := domain.Hash(secret)

	if !domain.SecretsMatch(secret, hash) {
		t.Error("SecretsMatch(secret, Hash(secret)) = false, want true")
	}
	if domain.SecretsMatch("wrong-secret", hash) {
		t.Error("SecretsMatch(wrong, hash) = true, want false")
	}
}
