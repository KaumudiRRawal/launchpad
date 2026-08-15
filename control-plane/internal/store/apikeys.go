package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/KaumudiRRawal/launchpad/control-plane/internal/domain"
)

// tokenScheme prefixes every issued token. A fixed, recognisable prefix means
// secret scanners can spot a leaked Launchpad key in a public commit.
const tokenScheme = "lp"

// GenerateToken mints a new API token and returns the plaintext alongside the
// public prefix and the hash to persist. The plaintext is returned to the
// caller exactly once and is never written down.
func GenerateToken() (token, prefix, hash string, err error) {
	// 4 bytes of public prefix to tell keys apart in a list, 32 bytes of
	// secret, which is well beyond guessing range.
	var prefixBytes [4]byte
	var secretBytes [32]byte
	if _, err := rand.Read(prefixBytes[:]); err != nil {
		return "", "", "", fmt.Errorf("generate token prefix: %w", err)
	}
	if _, err := rand.Read(secretBytes[:]); err != nil {
		return "", "", "", fmt.Errorf("generate token secret: %w", err)
	}

	prefix = tokenScheme + "_" + hex.EncodeToString(prefixBytes[:])
	secret := base64.RawURLEncoding.EncodeToString(secretBytes[:])
	token = prefix + "_" + secret

	return token, prefix, HashToken(token), nil
}

// HashToken returns the hex-encoded SHA-256 of a token. Tokens are compared by
// hash so the database never holds a usable credential.
//
// A plain hash is correct here where it would be wrong for a password: the
// token is 256 bits of uniform randomness, so there is no dictionary to attack
// and nothing for a slow KDF to defend against.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateAPIKey issues a key for an account. The returned plaintext token is
// the only copy that will ever exist.
func (r *Repository) CreateAPIKey(ctx context.Context, accountID, name string) (domain.APIKey, string, error) {
	token, prefix, hash, err := GenerateToken()
	if err != nil {
		return domain.APIKey{}, "", err
	}

	const query = `
		INSERT INTO api_keys (account_id, name, token_hash, token_prefix)
		VALUES ($1, $2, $3, $4)
		RETURNING id, account_id, name, token_prefix, created_at, last_used_at, revoked_at`

	var k domain.APIKey
	err = r.pool.QueryRow(ctx, query, accountID, name, hash, prefix).
		Scan(&k.ID, &k.AccountID, &k.Name, &k.TokenPrefix, &k.CreatedAt, &k.LastUsedAt, &k.RevokedAt)
	if err != nil {
		return domain.APIKey{}, "", fmt.Errorf("create api key: %w", translate(err))
	}
	return k, token, nil
}

// Authenticate resolves a plaintext token to its account, or returns
// domain.ErrNotFound if the token is unknown or revoked.
func (r *Repository) Authenticate(ctx context.Context, token string) (domain.Account, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return domain.Account{}, domain.ErrNotFound
	}

	const query = `
		SELECT k.id, a.id, a.email, a.name, a.created_at, a.updated_at
		FROM api_keys k
		JOIN accounts a ON a.id = k.account_id
		WHERE k.token_hash = $1 AND k.revoked_at IS NULL`

	var keyID string
	var a domain.Account
	err := r.pool.QueryRow(ctx, query, HashToken(token)).
		Scan(&keyID, &a.ID, &a.Email, &a.Name, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return domain.Account{}, translate(err)
	}

	r.touchAPIKey(ctx, keyID)
	return a, nil
}

// touchAPIKey records that a key was used, but only if the stored timestamp is
// already a minute stale. Writing on every request would add a database write
// per API call to buy precision nobody needs.
//
// Failures are ignored on purpose: last_used_at is a convenience for the user,
// and losing an update is not a reason to fail their request.
func (r *Repository) touchAPIKey(ctx context.Context, keyID string) {
	const query = `
		UPDATE api_keys
		SET last_used_at = now()
		WHERE id = $1
		  AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute')`

	_, _ = r.pool.Exec(ctx, query, keyID)
}

func (r *Repository) ListAPIKeys(ctx context.Context, accountID string) ([]domain.APIKey, error) {
	const query = `
		SELECT id, account_id, name, token_prefix, created_at, last_used_at, revoked_at
		FROM api_keys
		WHERE account_id = $1
		ORDER BY created_at DESC`

	rows, err := r.pool.Query(ctx, query, accountID)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", translate(err))
	}
	defer rows.Close()

	keys := []domain.APIKey{}
	for rows.Next() {
		var k domain.APIKey
		if err := rows.Scan(&k.ID, &k.AccountID, &k.Name, &k.TokenPrefix,
			&k.CreatedAt, &k.LastUsedAt, &k.RevokedAt); err != nil {
			return nil, fmt.Errorf("scan api key: %w", err)
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// RevokeAPIKey disables a key permanently. Revoking is idempotent only in
// effect, not in result: revoking an already-revoked key reports not found,
// so a caller cannot mistake it for having just taken effect.
func (r *Repository) RevokeAPIKey(ctx context.Context, accountID, id string) error {
	const query = `
		UPDATE api_keys SET revoked_at = now()
		WHERE id = $1 AND account_id = $2 AND revoked_at IS NULL`

	tag, err := r.pool.Exec(ctx, query, id, accountID)
	if err != nil {
		return fmt.Errorf("revoke api key: %w", translate(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("revoke api key: %w", domain.ErrNotFound)
	}
	return nil
}
