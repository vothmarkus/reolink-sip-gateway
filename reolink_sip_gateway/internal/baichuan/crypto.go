// Portions adapted from Shareed2k/reolinkproxy (MIT License).
// Copyright (c) 2026 Roman Kredentser. See THIRD-PARTY-NOTICES.md.
package baichuan

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5" // #nosec G501 -- protocol requirement
)

var (
	bcXMLKey = [...]byte{0x1F, 0x2D, 0x3C, 0x4B, 0x5A, 0x69, 0x78, 0xFF}
	aesIV    = []byte("0123456789abcdef")
)

func MD5Modern(input string) string {
	sum := md5.Sum([]byte(input)) // #nosec G401 -- protocol requirement
	const hex = "0123456789ABCDEF"
	out := make([]byte, len(sum)*2)
	for i, b := range sum {
		out[i*2] = hex[b>>4]
		out[i*2+1] = hex[b&0x0F]
	}
	if len(out) > 31 {
		out = out[:31]
	}
	return string(out)
}

func DeriveAESKey(nonce, password string) [16]byte {
	phrase := MD5Modern(nonce + "-" + password)
	var out [16]byte
	copy(out[:], []byte(phrase[:16]))
	return out
}

func BCXOR(offset uint8, buf []byte) []byte {
	out := make([]byte, len(buf))
	for i, b := range buf {
		key := bcXMLKey[(int(offset)+i)%len(bcXMLKey)]
		out[i] = b ^ key ^ offset
	}
	return out
}

func encryptXML(offset uint8, buf []byte, mode EncryptionMode, aesKey [16]byte, hasAESKey bool) []byte {
	switch mode {
	case EncryptionNone:
		return append([]byte(nil), buf...)
	case EncryptionBC:
		return BCXOR(offset, buf)
	case EncryptionAES:
		if !hasAESKey {
			return BCXOR(offset, buf)
		}
		return aesCFB(buf, aesKey, true)
	default:
		return append([]byte(nil), buf...)
	}
}

func decryptXML(offset uint8, buf []byte, mode EncryptionMode, aesKey [16]byte, hasAESKey bool) []byte {
	switch mode {
	case EncryptionNone:
		return append([]byte(nil), buf...)
	case EncryptionBC:
		return BCXOR(offset, buf)
	case EncryptionAES:
		if !hasAESKey {
			return BCXOR(offset, buf)
		}
		return aesCFB(buf, aesKey, false)
	default:
		return append([]byte(nil), buf...)
	}
}

func aesCFB(buf []byte, key [16]byte, encrypt bool) []byte {
	block, _ := aes.NewCipher(key[:])
	out := append([]byte(nil), buf...)
	if encrypt {
		stream := cipher.NewCFBEncrypter(block, aesIV) // #nosec G407 -- protocol requirement
		stream.XORKeyStream(out, out)
	} else {
		stream := cipher.NewCFBDecrypter(block, aesIV) // #nosec G407 -- protocol requirement
		stream.XORKeyStream(out, out)
	}
	return out
}
