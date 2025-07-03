package main

import (
	"context"
	"fmt"
	"github.com/cockroachdb/errors"
	types2 "github.com/cosmos/ibc-go/v8/modules/core/02-client/types"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/types"
	"github.com/datachainlab/optimism-ibc-relay-prover/tools/misbehaviour/l2"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/hyperledger-labs/yui-relayer/log"
	"math/big"
	"os"
	"time"
)

func main() {
	_ = log.InitLogger("debug", "text", "stdout", false)
	ctx := context.Background()
	if err := run(ctx); err != nil {
		fmt.Printf("Error: %+v\n", err)
	}
}

func run(ctx context.Context) error {
	config, err := l2.NewConfig(ctx)
	if err != nil {
		return errors.WithStack(err)
	}

	// Find latest game
	gameCount, err := config.DisputeGameFactoryCaller.GameCount(nil)
	if err != nil {
		return errors.WithStack(err)
	}
	start := gameCount.Int64() - 1
	gameType := uint32(1) // Permission Cannon in local netk
	results, err := config.DisputeGameFactoryCaller.FindLatestGames(nil, gameType, big.NewInt(start), big.NewInt(1))
	if err != nil {
		return errors.WithStack(err)
	}

	// Get finalized L1
	l1Header, err := config.ProverL1Client.GetLatestFinalizedL1Header(ctx)
	if err != nil {
		return errors.WithStack(err)
	}
	if err != nil {
		return errors.WithStack(err)
	}
	fmt.Printf("l1 state root=%s\n", common.Bytes2Hex(l1Header.ExecutionUpdate.StateRoot))

	l1InitialState, err := config.ProverL1Client.BuildInitialState(ctx, l1Header.ExecutionUpdate.BlockNumber)
	if err != nil {
		return errors.WithStack(err)
	}
	l1Config, err := config.ProverL1Client.BuildL1Config(l1InitialState, 0, 86400*time.Second)
	if err != nil {
		return errors.WithStack(err)
	}
	l1Header.TrustedSyncCommittee = &types.TrustedSyncCommittee{
		IsNext: false,
		SyncCommittee: &types.SyncCommittee{
			Pubkeys:         l1InitialState.CurrentSyncCommittee.Pubkeys,
			AggregatePubkey: l1InitialState.CurrentSyncCommittee.AggregatePubkey,
		},
	}

	// Get resolved
	resolvedL2, resolvedFaultDisputeGame, resolvedOutputRoot, caller, err := l2.CreateGameProof(ctx, gameType, config, l1Header.ExecutionUpdate, results[0])
	if err != nil {
		return errors.WithStack(err)
	}

	// Get sealedAt
	submittedL1, err := caller.L1Head(nil)
	if err != nil {
		return errors.WithStack(err)
	}

	submittedL1EthHeader, err := config.L1Client.HeaderByHash(ctx, submittedL1)
	if err != nil {
		return errors.WithStack(err)
	}
	submittedL1EthHeader, err = config.L1Client.HeaderByNumber(ctx, big.NewInt(submittedL1EthHeader.Number.Int64()+1))
	if err != nil {
		return errors.WithStack(err)
	}
	submittedL1 = submittedL1EthHeader.Hash()
	fmt.Printf("submittedL1=%s, currentL1=%d\n", submittedL1EthHeader.Number.String(), l1Header.ExecutionUpdate.BlockNumber)

	submittedL1Header := &types.L1Header{
		ExecutionUpdate: &types.ExecutionUpdate{
			BlockNumber: submittedL1EthHeader.Number.Uint64(),
			StateRoot:   submittedL1EthHeader.Root[:],
		},
	}
	_, sealedFaultDisputeGame, _, _, err := l2.CreateGameProof(ctx, gameType, config, submittedL1Header.ExecutionUpdate, results[0])
	if err != nil {
		return errors.WithStack(err)
	}

	l1History, err := createL1History(ctx, config, l1Header.ExecutionUpdate.BlockNumber, submittedL1EthHeader.Number)
	if err != nil {
		return errors.WithStack(err)
	}
	if len(l1History) == 0 {
		return errors.New("l1 history is empty")
	}

	misbehaviour := types.Misbehaviour{
		ClientId: "optimism-01",
		TrustedHeight: &types2.Height{
			RevisionNumber: 0,
			RevisionHeight: resolvedL2.Uint64() - 1,
		},
		LatestL1Header:        l1Header,
		ResolvedL2Number:      resolvedL2.Uint64(),
		ResolvedOutputRoot:    resolvedOutputRoot[:],
		FaultDisputeGameProof: resolvedFaultDisputeGame,
		SubmittedL1Proof:      sealedFaultDisputeGame,
		L1HeaderHistory:       l1History,
	}
	clientMessage, err := types2.PackClientMessage(&misbehaviour)
	if err != nil {
		return errors.WithStack(err)
	}

	cs := &types.ClientState{
		LatestHeight:           misbehaviour.TrustedHeight,
		L1Config:               l1Config,
		FaultDisputeGameConfig: config.ToFaultDisputeGameConfig(),
	}

	consState := types.ConsensusState{
		OutputRoot:             make([]byte, 32), // unused
		L1Slot:                 l1InitialState.Slot,
		L1CurrentSyncCommittee: l1InitialState.CurrentSyncCommittee.AggregatePubkey,
		L1NextSyncCommittee:    l1InitialState.NextSyncCommittee.AggregatePubkey,
		L1Timestamp:            l1InitialState.Timestamp,
		L1Origin:               submittedL1EthHeader.Number.Uint64() + 1,
		StorageRoot:            make([]byte, 32), // unused
	}

	misbehaviourBytes, err := clientMessage.Marshal()
	if err != nil {
		return errors.WithStack(err)
	}
	if err = os.WriteFile("submit_misbehaviour_future.bin", misbehaviourBytes, 0644); err != nil {
		return errors.WithStack(err)
	}

	csBytes, err := cs.Marshal()
	if err != nil {
		return errors.WithStack(err)
	}
	consStateBytes, err := consState.Marshal()
	if err != nil {
		return errors.WithStack(err)
	}
	fmt.Printf("ClientState: %s\n", common.Bytes2Hex(csBytes))
	fmt.Printf("ConsState: %s\n", common.Bytes2Hex(consStateBytes))
	fmt.Printf("now: %d\n", time.Now().Unix())

	misbehaviour.TrustedHeight.RevisionHeight = resolvedL2.Uint64()
	clientMessage, err = types2.PackClientMessage(&misbehaviour)
	if err != nil {
		return errors.WithStack(err)
	}
	misbehaviourBytes, err = clientMessage.Marshal()
	if err != nil {
		return errors.WithStack(err)
	}
	if err = os.WriteFile("submit_misbehaviour_not_misbehaviour_future.bin", misbehaviourBytes, 0644); err != nil {
		return errors.WithStack(err)
	}
	return nil
}

func createL1History(ctx context.Context, config *l2.Config, resolvedNum uint64, sealedNum *big.Int) ([][]byte, error) {

	history := make([][]byte, 0)
	diff := int64(resolvedNum) - sealedNum.Int64()
	for i := int64(0); i <= diff; i++ {
		header, err := config.L1Client.HeaderByNumber(ctx, big.NewInt(int64(resolvedNum)-i))
		if err != nil {
			return nil, errors.WithStack(err)
		}
		encoded, err := rlp.EncodeToBytes(header)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		history = append(history, encoded)
	}
	return history, nil
}
