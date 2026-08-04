package l1

import (
	"context"

	"github.com/datachainlab/ethereum-light-client-types/prover/beacon"
	lcrelay "github.com/datachainlab/ethereum-light-client-types/prover/relay"
	lctypes "github.com/datachainlab/ethereum-light-client-types/prover/types"
)

func (pr *L1Client) secondsPerSlot() uint64 {
	return lcrelay.SecondsPerSlot(pr.config.Network)
}

func (pr *L1Client) slotsPerEpoch() uint64 {
	return lcrelay.SlotsPerEpoch(pr.config.Network)
}

func (pr *L1Client) epochsPerSyncCommitteePeriod() uint64 {
	return lcrelay.EpochsPerSyncCommitteePeriod(pr.config.Network)
}

func (pr *L1Client) getPeriodBoundarySlot(period uint64) uint64 {
	return lcrelay.GetPeriodBoundarySlot(pr.config.Network, period)
}

func (pr *L1Client) computeSyncCommitteePeriod(epoch uint64) uint64 {
	return lcrelay.ComputeSyncCommitteePeriod(pr.config.Network, epoch)
}

func (pr *L1Client) computeEpoch(slot uint64) uint64 {
	return lcrelay.ComputeEpoch(pr.config.Network, slot)
}

func (pr *L1Client) getSlotAtTimestamp(ctx context.Context, timestamp uint64) (uint64, error) {
	return lcrelay.GetSlotAtTimestamp(ctx, pr.beaconClient, pr.config.Network, timestamp)
}

func (pr *L1Client) getPeriodWithBlockNumber(ctx context.Context, blockNumber uint64) (uint64, error) {
	return lcrelay.GetPeriodWithBlockNumber(ctx, pr.beaconClient, pr.executionClient, pr.config.Network, blockNumber)
}

func (pr *L1Client) buildExecutionUpdateFromFinalizedHeader(ctx context.Context, finalizedHeader *beacon.LightClientHeader) (*lctypes.ExecutionUpdate, uint64, error) {
	return lcrelay.BuildExecutionUpdateFromFinalizedHeader(ctx, pr.executionRawClient, finalizedHeader, true)
}
