package l1

import (
	"context"
	"fmt"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/prover/l1/beacon"
	lctypes "github.com/datachainlab/optimism-ibc-relay-prover/module/types"
	"math/big"
)

const (
	GENESIS_SLOT = 0
)

// merkle tree's leaf index
const (
	EXECUTION_STATE_ROOT_LEAF_INDEX   = 2
	EXECUTION_BLOCK_NUMBER_LEAF_INDEX = 6
)

// minimal preset
const (
	MINIMAL_SECONDS_PER_SLOT                 uint64 = 6
	MINIMAL_SLOTS_PER_EPOCH                  uint64 = 8
	MINIMAL_EPOCHS_PER_SYNC_COMMITTEE_PERIOD uint64 = 8
)

// mainnet preset
const (
	MAINNET_SECONDS_PER_SLOT                 uint64 = 12
	MAINNET_SLOTS_PER_EPOCH                  uint64 = 32
	MAINNET_EPOCHS_PER_SYNC_COMMITTEE_PERIOD uint64 = 256
)

func (pr *L1Client) secondsPerSlot() uint64 {
	if pr.config.IsMainnetPreset() {
		return MAINNET_SECONDS_PER_SLOT
	} else {
		return MINIMAL_SECONDS_PER_SLOT
	}
}

func (pr *L1Client) slotsPerEpoch() uint64 {
	if pr.config.IsMainnetPreset() {
		return MAINNET_SLOTS_PER_EPOCH
	} else {
		return MINIMAL_SLOTS_PER_EPOCH
	}
}

func (pr *L1Client) epochsPerSyncCommitteePeriod() uint64 {
	if pr.config.IsMainnetPreset() {
		return MAINNET_EPOCHS_PER_SYNC_COMMITTEE_PERIOD
	} else {
		return MINIMAL_EPOCHS_PER_SYNC_COMMITTEE_PERIOD
	}
}

// returns the first slot of the period
func (pr *L1Client) getPeriodBoundarySlot(period uint64) uint64 {
	return period * pr.epochsPerSyncCommitteePeriod() * pr.slotsPerEpoch()
}

func (pr *L1Client) computeSyncCommitteePeriod(epoch uint64) uint64 {
	return epoch / pr.epochsPerSyncCommitteePeriod()
}

func (pr *L1Client) computeEpoch(slot uint64) uint64 {
	return slot / pr.slotsPerEpoch()
}

func (pr *L1Client) getSlotAtTimestamp(ctx context.Context, timestamp uint64) (uint64, error) {
	genesis, err := pr.beaconClient.GetGenesis(ctx)
	if err != nil {
		return 0, err
	}
	if timestamp < genesis.GenesisTimeSeconds {
		return 0, fmt.Errorf("computeSlotAtTimestamp: timestamp is smaller than genesisTime: timestamp=%v genesisTime=%v", timestamp, genesis.GenesisTimeSeconds)
	} else if (timestamp-genesis.GenesisTimeSeconds)%pr.secondsPerSlot() != 0 {
		return 0, fmt.Errorf("computeSlotAtTimestamp: timestamp is not multiple of secondsPerSlot: timestamp=%v secondsPerSlot=%v genesisTime=%v", timestamp, pr.secondsPerSlot(), genesis.GenesisTimeSeconds)
	}
	slotsSinceGenesis := (timestamp - genesis.GenesisTimeSeconds) / pr.secondsPerSlot()
	return GENESIS_SLOT + slotsSinceGenesis, nil
}

// returns a period corresponding to a given execution block number
func (pr *L1Client) getPeriodWithBlockNumber(ctx context.Context, blockNumber uint64) (uint64, error) {
	header, err := pr.executionClient.HeaderByNumber(ctx, big.NewInt(0).SetUint64(blockNumber))
	if err != nil {
		return 0, err
	}

	slot, err := pr.getSlotAtTimestamp(ctx, header.Time)
	if err != nil {
		return 0, err
	}
	return pr.computeSyncCommitteePeriod(pr.computeEpoch(slot)), nil
}

func (pr *L1Client) buildExecutionUpdate(executionHeader *beacon.ExecutionPayloadHeader) (*lctypes.ExecutionUpdate, error) {
	stateRootBranch, err := generateExecutionPayloadHeaderProof(executionHeader, EXECUTION_STATE_ROOT_LEAF_INDEX)
	if err != nil {
		return nil, err
	}
	blockNumberBranch, err := generateExecutionPayloadHeaderProof(executionHeader, EXECUTION_BLOCK_NUMBER_LEAF_INDEX)
	if err != nil {
		return nil, err
	}
	blockHashBranch, err := generateExecutionPayloadHeaderProof(executionHeader, EXECUTION_BLOCK_NUMBER_LEAF_INDEX+6)
	if err != nil {
		return nil, err
	}
	return &lctypes.ExecutionUpdate{
		StateRoot:         executionHeader.StateRoot,
		StateRootBranch:   stateRootBranch,
		BlockNumber:       executionHeader.BlockNumber,
		BlockNumberBranch: blockNumberBranch,
		BlockHash:         executionHeader.BlockHash,
		BlockHashBranch:   blockHashBranch,
	}, nil
}
