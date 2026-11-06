package eris

import "golang.org/x/crypto/blake2b"

func blake2bSum(data []byte) [32]byte {
	return blake2b.Sum256(data)
}
