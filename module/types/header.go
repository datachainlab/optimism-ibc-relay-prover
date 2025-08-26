package types

import (
	"fmt"
	clienttypes "github.com/cosmos/ibc-go/v8/modules/core/02-client/types"
	"github.com/cosmos/ibc-go/v8/modules/core/exported"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/util"
	"github.com/hyperledger-labs/yui-relayer/core"
)

var _ core.Header = (*Header)(nil)

func (*Header) ClientType() string {
	return ClientType
}

func (h *Header) GetHeight() exported.Height {
	return clienttypes.NewHeight(0, h.Derivation.L2BlockNumber)
}

func (h *Header) ValidateBasic() error {
	return nil
}

func (h *Header) ToLog() []interface{} {
	trustedToDeterministicNums := util.Map(h.TrustedToDeterministic, func(item *L1Header, index int) string {
		if item == nil || item.ConsensusUpdate == nil || item.ConsensusUpdate.FinalizedHeader == nil || item.TrustedSyncCommittee == nil {
			return ""
		}
		return fmt.Sprintf("%d/%t", item.ConsensusUpdate.FinalizedHeader.Slot, item.TrustedSyncCommittee.IsNext)
	})
	deterministicToLatestNums := util.Map(h.DeterministicToLatest, func(item *L1Header, index int) string {
		if item == nil || item.ConsensusUpdate == nil || item.ConsensusUpdate.FinalizedHeader == nil || item.TrustedSyncCommittee == nil {
			return ""
		}
		return fmt.Sprintf("%d/%t", item.ConsensusUpdate.FinalizedHeader.Slot, item.TrustedSyncCommittee.IsNext)
	})
	var args []interface{}
	if h.Derivation != nil {
		args = append(args, "l2", h.Derivation.L2BlockNumber)
	}
	if h.TrustedHeight != nil {
		args = append(args, "trusted_l2", h.TrustedHeight.GetRevisionHeight())
	}
	return append(args, "l1_t2d", trustedToDeterministicNums, "l1_d2l", deterministicToLatestNums)
}
