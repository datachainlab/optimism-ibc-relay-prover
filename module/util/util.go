package util

import clienttypes "github.com/cosmos/ibc-go/v8/modules/core/02-client/types"

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

func Group[T any](input []*T, size int) [][]*T {
	if size <= 0 {
		return nil
	}

	var chunks [][]*T
	for i := 0; i < len(input); i += size {
		end := i + size
		if end > len(input) {
			end = len(input)
		}
		chunks = append(chunks, input[i:end])
	}
	return chunks
}
