package types

import (
	clienttypes "github.com/cosmos/ibc-go/v8/modules/core/02-client/types"
	"github.com/cosmos/ibc-go/v8/modules/core/exported"
)

func (*Header) ClientType() string {
	return ClientType
}

func (h *Header) GetHeight() exported.Height {
	return clienttypes.NewHeight(0, h.Derivations[0].L2BlockNumber)
}

func (h *Header) ValidateBasic() error {
	return nil
}
