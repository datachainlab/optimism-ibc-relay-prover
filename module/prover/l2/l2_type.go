package l2

import (
	"reflect"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// https://github.com/ethereum-optimism/optimism/blob/v1.13.1/op-service/eth/sync_status.go
type SyncStatus struct {
	// CurrentL1 is the L1 block that the derivation process is last idled at.
	// This may not be fully derived into L2 data yet.
	// The safe L2 blocks were produced/included fully from the L1 chain up to _but excluding_ this L1 block.
	// If the node is synced, this matches the HeadL1, minus the verifier confirmation distance.
	CurrentL1 L1BlockRef `json:"current_l1"`
	// CurrentL1Finalized is a legacy sync-status attribute. This is deprecated.
	// A previous version of the L1 finalization-signal was updated only after the block was retrieved by number.
	// This attribute just matches FinalizedL1 now.
	CurrentL1Finalized L1BlockRef `json:"current_l1_finalized"`
	// HeadL1 is the perceived head of the L1 chain, no confirmation distance.
	// The head is not guaranteed to build on the other L1 sync status fields,
	// as the node may be in progress of resetting to adapt to a L1 reorg.
	HeadL1      L1BlockRef `json:"head_l1"`
	SafeL1      L1BlockRef `json:"safe_l1"`
	FinalizedL1 L1BlockRef `json:"finalized_l1"`
	// UnsafeL2 is the absolute tip of the L2 chain,
	// pointing to block data that has not been submitted to L1 yet.
	// The sequencer is building this, and verifiers may also be ahead of the
	// SafeL2 block if they sync blocks via p2p or other offchain sources.
	// This is considered to only be local-unsafe post-interop, see CrossUnsafe for cross-L2 guarantees.
	UnsafeL2 L2BlockRef `json:"unsafe_l2"`
	// SafeL2 points to the L2 block that was derived from the L1 chain.
	// This point may still reorg if the L1 chain reorgs.
	// This is considered to be cross-safe post-interop, see LocalSafe to ignore cross-L2 guarantees.
	SafeL2 L2BlockRef `json:"safe_l2"`
	// FinalizedL2 points to the L2 block that was derived fully from
	// finalized L1 information, thus irreversible.
	FinalizedL2 L2BlockRef `json:"finalized_l2"`
	// PendingSafeL2 points to the L2 block processed from the batch, but not consolidated to the safe block yet.
	PendingSafeL2 L2BlockRef `json:"pending_safe_l2"`
	// CrossUnsafeL2 is an unsafe L2 block, that has been verified to match cross-L2 dependencies.
	// Pre-interop every unsafe L2 block is also cross-unsafe.
	CrossUnsafeL2 L2BlockRef `json:"cross_unsafe_l2"`
	// LocalSafeL2 is an L2 block derived from L1, not yet verified to have valid cross-L2 dependencies.
	LocalSafeL2 L2BlockRef `json:"local_safe_l2"`
}

type L1BlockRef struct {
	Hash       common.Hash `json:"hash"`
	Number     uint64      `json:"number"`
	ParentHash common.Hash `json:"parentHash"`
	Time       uint64      `json:"timestamp"`
}

type L2BlockRef struct {
	Hash           common.Hash `json:"hash"`
	Number         uint64      `json:"number"`
	ParentHash     common.Hash `json:"parentHash"`
	Time           uint64      `json:"timestamp"`
	L1Origin       BlockID     `json:"l1origin"`
	SequenceNumber uint64      `json:"sequenceNumber"` // distance to first block of epoch
}

type BlockID struct {
	Hash   common.Hash `json:"hash"`
	Number uint64      `json:"number"`
}

type OutputResponse struct {
	Version               Bytes32     `json:"version"`
	OutputRoot            Bytes32     `json:"outputRoot"`
	BlockRef              L2BlockRef  `json:"blockRef"`
	WithdrawalStorageRoot common.Hash `json:"withdrawalStorageRoot"`
	StateRoot             common.Hash `json:"stateRoot"`
	Status                *SyncStatus `json:"syncStatus"`
}

type Bytes32 [32]byte

func (b *Bytes32) UnmarshalJSON(text []byte) error {
	return hexutil.UnmarshalFixedJSON(reflect.TypeOf(b), text, b[:])
}

type PreimageMetadata struct {
	Agreed  uint64      `json:"agreed"`
	Claimed uint64      `json:"claimed"`
	L1Head  common.Hash `json:"l1_head"`
}
