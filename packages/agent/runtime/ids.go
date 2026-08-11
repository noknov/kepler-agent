package runtime

import (
	"crypto/rand"
	"encoding/hex"
)

type IDGenerator interface {
	New(prefix string) string
}

type RandomIDs struct{}

func (RandomIDs) New(prefix string) string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(value[:])
}
