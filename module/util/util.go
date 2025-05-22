package util

import clienttypes "github.com/cosmos/ibc-go/v8/modules/core/02-client/types"

func NewHeight(blockNumber uint64) *clienttypes.Height {
	h := clienttypes.NewHeight(0, blockNumber)
	return &h
}
