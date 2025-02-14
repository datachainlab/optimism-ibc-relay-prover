package l1

import (
	"context"
	"fmt"
	"github.com/datachainlab/ethereum-ibc-relay-prover/beacon"
	lctypes "github.com/datachainlab/ethereum-ibc-relay-prover/light-clients/ethereum/types"
	ethprover "github.com/datachainlab/ethereum-ibc-relay-prover/relay"
)

func (pr *L1Client) secondsPerSlot() uint64 {
	return ethprover.SecondsPerSlot(ethprover.IsMainnetPreset(pr.network))
}

func (pr *L1Client) slotsPerEpoch() uint64 {
	return ethprover.SlotsPerEpoch(ethprover.IsMainnetPreset(pr.network))
}

func (pr *L1Client) epochsPerSyncCommitteePeriod() uint64 {
	return ethprover.EpochsPerSyncCommitteePeriod(ethprover.IsMainnetPreset(pr.network))
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

func (pr *L1Client) GetSlotAtTimestamp(timestamp uint64) (uint64, error) {
	genesis, err := pr.beaconClient.GetGenesis()
	if err != nil {
		return 0, err
	}
	if timestamp < genesis.GenesisTimeSeconds {
		return 0, fmt.Errorf("computeSlotAtTimestamp: timestamp is smaller than genesisTime: timestamp=%v genesisTime=%v", timestamp, genesis.GenesisTimeSeconds)
	} else if (timestamp-genesis.GenesisTimeSeconds)%pr.secondsPerSlot() != 0 {
		return 0, fmt.Errorf("computeSlotAtTimestamp: timestamp is not multiple of secondsPerSlot: timestamp=%v secondsPerSlot=%v genesisTime=%v", timestamp, pr.secondsPerSlot(), genesis.GenesisTimeSeconds)
	}
	slotsSinceGenesis := (timestamp - genesis.GenesisTimeSeconds) / pr.secondsPerSlot()
	return ethprover.GENESIS_SLOT + slotsSinceGenesis, nil
}

// returns a period corresponding to a given execution block number
func (pr *L1Client) getPeriodWithBlockNumber(blockNumber uint64) (uint64, error) {
	timestamp, err := pr.TimestampAt(context.Background(), blockNumber)
	if err != nil {
		return 0, err
	}
	slot, err := pr.GetSlotAtTimestamp(timestamp)
	if err != nil {
		return 0, err
	}
	return pr.computeSyncCommitteePeriod(pr.computeEpoch(slot)), nil
}

func (pr *L1Client) buildExecutionUpdate(executionHeader *beacon.ExecutionPayloadHeader) (*lctypes.ExecutionUpdate, error) {
	return ethprover.BuildExecutionUpdate(executionHeader)
}
