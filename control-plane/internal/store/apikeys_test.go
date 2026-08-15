package store

import (
	"strings"
	"testing"
)

func TestGenerateToken(t *testing.T) {
	token, prefix, hash, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}

	t.Run("token carries the scannable scheme prefix", func(t *testing.T) {
		// A fixed, recognisable prefix is what lets secret scanners catch a
		// key leaked into a public commit.
		if !strings.HasPrefix(token, tokenScheme+"_") {
			t.Errorf("token %q does not start with %q", token, tokenScheme+"_")
		}
		if !strings.HasPrefix(token, prefix) {
			t.Errorf("token %q does not start with its prefix %q", token, prefix)
		}
	})

	t.Run("prefix is public and reveals no secret", func(t *testing.T) {
		// The prefix is displayed in the dashboard, so the remaining secret
		// must be substantial.
		secret := strings.TrimPrefix(token, prefix+"_")
		if len(secret) < 40 {
			t.Errorf("secret segment is %d characters, want at least 40", len(secret))
		}
	})

	t.Run("hash matches the token and is not the token", func(t *testing.T) {
		if hash != HashToken(token) {
			t.Error("returned hash does not match HashToken(token)")
		}
		if strings.Contains(hash, token) || hash == token {
			t.Error("hash contains the plaintext token")
		}
		if len(hash) != 64 {
			t.Errorf("hash is %d characters, want 64 hex characters", len(hash))
		}
	})
}

func TestGenerateTokenIsUnique(t *testing.T) {
	const iterations = 500

	seen := make(map[string]bool, iterations)
	for i := range iterations {
		token, _, _, err := GenerateToken()
		if err != nil {
			t.Fatalf("GenerateToken() error on iteration %d = %v", i, err)
		}
		if seen[token] {
			t.Fatalf("GenerateToken() produced a duplicate on iteration %d", i)
		}
		seen[token] = true
	}
}

func TestHashTokenIsDeterministic(t *testing.T) {
	const token = "lp_deadbeef_some-secret-value"

	if HashToken(token) != HashToken(token) {
		t.Error("HashToken is not deterministic")
	}
	if HashToken(token) == HashToken(token+"x") {
		t.Error("different tokens hashed to the same value")
	}
}
