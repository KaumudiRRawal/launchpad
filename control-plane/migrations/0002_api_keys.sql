-- API keys authenticate every request to the control plane.
--
-- Only a SHA-256 hash of the token is stored. A leaked database dump therefore
-- yields no usable credentials, and the plaintext token is shown to the caller
-- exactly once, at creation.
CREATE TABLE api_keys (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id   UUID        NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    name         TEXT        NOT NULL,
    -- Hex-encoded SHA-256 of the full token. Unique so a hash collision or a
    -- duplicate insert fails loudly rather than silently sharing a credential.
    token_hash   TEXT        NOT NULL UNIQUE,
    -- Leading public segment of the token, e.g. "lp_a1b2c3d4". Safe to display
    -- so a user can tell their keys apart without revealing the secret.
    token_prefix TEXT        NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ
);

CREATE INDEX api_keys_account_id_idx ON api_keys (account_id);

-- Authentication looks a key up by hash on every single request, and only
-- live keys can authenticate.
CREATE INDEX api_keys_active_hash_idx
    ON api_keys (token_hash)
    WHERE revoked_at IS NULL;

CREATE TRIGGER api_keys_set_updated_at BEFORE UPDATE ON api_keys
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
