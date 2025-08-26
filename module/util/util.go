package util

import (
	clienttypes "github.com/cosmos/ibc-go/v8/modules/core/02-client/types"
	"math/big"
)

func NewHeight(blockNumber uint64) *clienttypes.Height {
	h := clienttypes.NewHeight(0, blockNumber)
	return &h
}

func Map[T any, R any](collection []T, iteratee func(item T, index int) R) []R {
	result := make([]R, len(collection))

	for i := range collection {
		result[i] = iteratee(collection[i], i)
	}

	return result
}

func NewBigInt(value uint64) *big.Int {
	return new(big.Int).SetUint64(value)
}
