// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package aes

import (
	"bytes"
	"crypto/internal/fips"
	_ "crypto/internal/fips/check"
	"errors"
)

func init() {
	fips.CAST("AES-CBC", func() error {
		key := []byte{
			0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
			0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10,
		}
		iv := [16]byte{
			0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18,
			0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20,
		}
		plaintext := []byte{
			0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28,
			0x29, 0x2a, 0x2b, 0x2c, 0x2d, 0x2e, 0x2f, 0x30,
		}
		ciphertext := []byte{
			0xdf, 0x76, 0x26, 0x4b, 0xd3, 0xb2, 0xc4, 0x8d,
			0x40, 0xa2, 0x6e, 0x7a, 0xc4, 0xff, 0xbd, 0x35,
		}
		b, err := New(key)
		if err != nil {
			return err
		}
		buf := make([]byte, 16)
		NewCBCEncrypter(b, iv).CryptBlocks(buf, plaintext)
		if !bytes.Equal(buf, ciphertext) {
			return errors.New("unexpected result")
		}
		NewCBCDecrypter(b, iv).CryptBlocks(buf, ciphertext)
		if !bytes.Equal(buf, plaintext) {
			return errors.New("unexpected result")
		}
		return nil
	})
}
