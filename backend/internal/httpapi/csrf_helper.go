package httpapi

import (
	"crypto/rand"
	"encoding/hex"
)

func cryptoRead(b []byte) (int, error) { return rand.Read(b) }

func hexEncode(b []byte) string { return hex.EncodeToString(b) }
