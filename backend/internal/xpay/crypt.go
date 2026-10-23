package xpay

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
)

// MessagePush implements WeChat message-push encryption (安全模式),
// ported from overtrue/wechat Kernel/Encryptor: AES-256-CBC with
// key=b64decode(aesKey), iv=key[:16], plaintext = 16B random + 4B len
// (big-endian) + body + appId, PKCS#7 padded to 32B blocks.
type MessagePush struct {
	AppID  string
	Token  string
	AESKey string // base64, 43 chars => 32 bytes
}

func (m *MessagePush) key() ([]byte, error) {
	if len(m.AESKey) < 43 {
		return nil, errors.New("AESKey 长度非法")
	}
	return base64.StdEncoding.DecodeString(m.AESKey + "=")
}

func pkcs7Pad(b []byte, blockSize int) []byte {
	pad := blockSize - len(b)%blockSize
	out := make([]byte, len(b)+pad)
	copy(out, b)
	for i := len(b); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}

func pkcs7Unpad(b []byte, blockSize int) ([]byte, error) {
	if len(b) == 0 || len(b)%blockSize != 0 {
		return nil, errors.New("invalid padding size")
	}
	pad := int(b[len(b)-1])
	if pad <= 0 || pad > blockSize || pad > len(b) {
		return nil, errors.New("invalid padding")
	}
	for _, c := range b[len(b)-pad:] {
		if int(c) != pad {
			return nil, errors.New("invalid padding bytes")
		}
	}
	return b[:len(b)-pad], nil
}

// Decrypt verifies msg_signature (sha1 sort token/timestamp/nonce/encrypt)
// and returns the plaintext body.
func (m *MessagePush) Decrypt(encryptB64, msgSignature, timestamp, nonce string) ([]byte, error) {
	if !VerifyPlaintextPostSig(m.Token, msgSignature, timestamp, nonce, encryptB64) {
		return nil, errors.New("消息推送签名校验失败")
	}
	key, err := m.key()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	cipherText, err := base64.StdEncoding.DecodeString(encryptB64)
	if err != nil || len(cipherText) < aes.BlockSize || len(cipherText)%aes.BlockSize != 0 {
		return nil, errors.New("密文格式非法")
	}
	mode := cipher.NewCBCDecrypter(block, key[:16])
	plain := make([]byte, len(cipherText))
	mode.CryptBlocks(plain, cipherText)
	unpadded, err := pkcs7Unpad(plain, aes.BlockSize)
	if err != nil {
		return nil, err
	}
	if len(unpadded) < 20 {
		return nil, errors.New("解密报文过短")
	}
	body := unpadded[16:]
	msgLen := binary.BigEndian.Uint32(body[:4])
	if int(msgLen)+4 > len(body) {
		return nil, errors.New("解密报文长度非法")
	}
	msg := body[4 : 4+msgLen]
	if string(body[4+msgLen:]) != m.AppID {
		return nil, errors.New("appId 校验失败")
	}
	return msg, nil
}

// Encrypt produces the encrypted reply body: a JSON wrapper with
// Encrypt/MsgSignature/TimeStamp/Nonce fields (JSON push keeps the same
// field set as the XML wrapper, per the legacy buildVirtualIosRefundQueryResponse).
func (m *MessagePush) Encrypt(plain string, nonce string, timestamp string) (map[string]string, error) {
	key, err := m.key()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return nil, err
	}
	lenBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lenBuf, uint32(len(plain)))
	raw := append([]byte{}, random...)
	raw = append(raw, lenBuf...)
	raw = append(raw, []byte(plain)...)
	raw = append(raw, []byte(m.AppID)...)
	padded := pkcs7Pad(raw, aes.BlockSize)

	enc := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, key[:16]).CryptBlocks(enc, padded)
	encryptB64 := base64.StdEncoding.EncodeToString(enc)
	sig := sha1Sig(m.Token, timestamp, nonce, encryptB64)
	return map[string]string{
		"Encrypt":      encryptB64,
		"MsgSignature": sig,
		"TimeStamp":    timestamp,
		"Nonce":        nonce,
	}, nil
}

func sha1Sig(parts ...string) string {
	joined := concatSorted(parts)
	sum := sha1Sum([]byte(joined))
	return fmt.Sprintf("%x", sum)
}
