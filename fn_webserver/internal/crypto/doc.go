// Package crypto centralizes ALL cryptographic operations.
//
// SECURITY SCAFFOLDING MAP (didactic overview):
//   password.go — Argon2id + PER-PASSWORD RANDOM SALT  (defends DB leaks)
//   token.go    — HS256 JWT + CSPRNG refresh tokens    (defends session theft)
//   aes.go      — AES-256-GCM envelope encryption      (defends PII at rest)
//
// RULES I followed so you can keep following them:
//  1. Never roll your own primitives — use stdlib/x/crypto.
//  2. Never compare secrets with == ; use subtle.ConstantTimeCompare.
//  3. Never reuse a nonce with the same key (AES-GCM would break catastrophically).
//  4. Salts/nonces come from crypto/rand (CSPRNG), never math/rand.
package crypto
