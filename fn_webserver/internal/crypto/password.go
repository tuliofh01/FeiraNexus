package crypto

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// ---------------------------------------------------------------------------
// Argon2id password hashing WITH PER-PASSWORD RANDOM SALT
// ---------------------------------------------------------------------------
//
// DIDACTIC — why salting matters (this is the "salting" you asked for):
//
// Without salt, two users with password "hunter2" produce identical hashes —
// an attacker can precompute (rainbow table) or crack both at once. A UNIQUE
// random salt per password forces the attacker to crack every hash separately.
// The salt is NOT secret (it's stored next to the hash) — its only job is
// uniqueness.
//
// PHC string format we store in users.password_hash:
//   $argon2id$v=19$m=65536,t=3,p=4$<base64(salt)>$<base64(hash)>
// This self-describes its parameters, so you can RAISE the cost later and
// still verify old hashes (parse params from the string at verify time).
//
// Argon2id = winner of the Password Hashing Competition; "id" variant resists
// both GPU and side-channel attacks.

const (
	argonTime      uint32 = 3         // iterations (t) — raise over time
	argonMemoryKiB uint32 = 64 * 1024 // 64 MiB (m)     — main CPU/memory cost
	argonThreads   uint8  = 4         // parallelism (p)
	argonKeyLen    uint32 = 32        // output length (32 bytes = 256 bits)
	argonSaltLen          = 16        // 128-bit salt (per-password, random)
)

// HashPassword derives an Argon2id hash of password with a fresh random salt
// and returns the full PHC-encoded string ready for users.password_hash.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("crypto: empty password")
	}

	// crypto/rand = cryptographically secure PRNG (OS entropy).
	// NEVER use math/rand for salts/keys/nonces — it's predictable.
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("crypto: generating salt: %w", err)
	}

	derived := argon2.IDKey([]byte(password), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)

	// base64 raw URL encoding (no padding) keeps the string compact & URL-safe.
	b64Salt := base64.RawStdEncoding.EncodeToString(salt)
	b64Hash := base64.RawStdEncoding.EncodeToString(derived)

	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonTime, argonThreads, b64Salt, b64Hash,
	), nil
}

// VerifyPassword checks password against a stored PHC string.
// Returns nil on match, non-nil otherwise.
func VerifyPassword(encodedHash, password string) error {
	if encodedHash == "" || password == "" {
		return errors.New("crypto: missing hash or password")
	}

	// Parse the PHC string: $argon2id$v=19$m=...,t=...,p=...$salt$hash
	parts := strings.Split(encodedHash, "$")
	// parts[0] is "" (string starts with $), so: ["", "argon2id", "v=19", "m=...", salt, hash]
	if len(parts) != 6 {
		return errors.New("crypto: malformed password hash format")
	}
	if parts[1] != "argon2id" {
		return errors.New("crypto: unsupported hash algorithm")
	}

	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return fmt.Errorf("crypto: parsing hash params: %w", err)
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return fmt.Errorf("crypto: decoding salt: %w", err)
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return fmt.Errorf("crypto: decoding hash: %w", err)
	}

	derived := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(expected)))

	// Constant-time compare: a normal == can leak information via timing
	// (attacker measures how long the compare takes to guess byte by byte).
	// subtle.ConstantTimeCompare always takes the same time regardless of
	// where the first difference is.
	if subtle.ConstantTimeCompare(derived, expected) != 1 {
		return errors.New("crypto: password mismatch")
	}
	return nil
}

// SUGGESTION (future work for you):
//   - Benchmark HashPassword. If logins feel slow at load, tune the params —
//     OWASP recommends ~50-100ms per hash on your production hardware.
//   - Add a rehash-on-login path: if the stored params are older/weaker than
//     current constants, re-hash and UPDATE the row transparently.
//   - Never log passwords or include them in error messages (they're in this
//     file's call graph only — keep it that way).
