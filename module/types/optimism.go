package types

import "github.com/ethereum/go-ethereum/common"

func (h *L1Header) ToLogValue() []interface{} {
	args := []interface{}{
		"l1", h.ExecutionUpdate.BlockNumber,
		"l1-finalized-slot", h.ConsensusUpdate.FinalizedHeader.Slot,
		"l1-signature-slot", h.ConsensusUpdate.SignatureSlot,
		"l1-is-next", h.TrustedSyncCommittee.IsNext,
		"l1-hash", common.BytesToHash(h.ExecutionUpdate.BlockHash),
		"l1-trusted-sc", common.Bytes2Hex(h.TrustedSyncCommittee.SyncCommittee.AggregatePubkey),
	}
	if h.ConsensusUpdate.NextSyncCommittee != nil {
		args = append(args, "l1-next-sc", common.Bytes2Hex(h.ConsensusUpdate.NextSyncCommittee.AggregatePubkey))
	}
	return args
}
