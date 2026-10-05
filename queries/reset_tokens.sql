-- name: DeleteOpenResetTokens :exec
DELETE FROM password_reset_tokens
 WHERE user_id = @user_id AND used_at IS NULL;

-- name: InsertResetToken :exec
INSERT INTO password_reset_tokens (token_hash, tenant_id, user_id, expires_at)
VALUES (@token_hash, @tenant_id, @user_id, @expires_at);

-- Single use is decided by the database, not by a read followed by a write:
-- only the first concurrent caller finds a row to mark used.
-- name: ConsumeResetToken :one
UPDATE password_reset_tokens
   SET used_at = now()
 WHERE token_hash = @token_hash AND used_at IS NULL AND expires_at > now()
RETURNING user_id;
