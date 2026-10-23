package xpay

import (
	"crypto/sha1"
	"encoding/hex"
	"sort"
	"strings"
)

// VerifyPlaintextSig checks the GET URL-verification and plaintext
// message-push signature: sha1(sort(token, timestamp, nonce)).
// Ported from Notify::assertPlaintextSignature.
func VerifyPlaintextSig(token, signature, timestamp, nonce string) bool {
	parts := []string{token, timestamp, nonce}
	sort.Strings(parts)
	sum := sha1.Sum([]byte(strings.Join(parts, "")))
	return hex.EncodeToString(sum[:]) == signature
}

// VerifyPlaintextPostSig checks the msg_signature of a plaintext (non
// secure-mode) JSON push: sha1(sort(token, timestamp, nonce, encryptBody)).
func VerifyPlaintextPostSig(token, msgSignature, timestamp, nonce, encryptOrPlainBody string) bool {
	parts := []string{token, timestamp, nonce, encryptOrPlainBody}
	sort.Strings(parts)
	sum := sha1.Sum([]byte(strings.Join(parts, "")))
	return hex.EncodeToString(sum[:]) == msgSignature
}
