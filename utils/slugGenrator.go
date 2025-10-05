package utils

import (
	"crypto/rand"
	"math/big"
)

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// CryptoRandomString returns a securely generated random string of length n.
// Good for slugs, tokens, IDs where predictability must be avoided.
func CryptoRandomString(n int) (string, error) {
	if n <= 0 {
		return "", nil
	}
	out := make([]byte, n)
	max := big.NewInt(int64(len(base62)))
	for i := 0; i < n; i++ {
		num, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		out[i] = base62[num.Int64()]
	}
	return string(out), nil
}
