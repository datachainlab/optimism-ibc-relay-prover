package main

import (
	"context"
	"fmt"
	"github.com/cockroachdb/errors"
	types2 "github.com/cosmos/ibc-go/v8/modules/core/02-client/types"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/types"
	"github.com/datachainlab/optimism-ibc-relay-prover/tools/misbehaviour/l2"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/hyperledger-labs/yui-relayer/log"
	"math/big"
	"os"
	"time"
)

func main() {
	_ = log.InitLogger("debug", "text", "stdout")
	ctx := context.Background()
	if err := run(ctx); err != nil {
		fmt.Printf("Error: %v\n", err)
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
	start := gameCount.Int64() - 5
	gameType := uint32(1) // Permission Cannon in local net
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
	resolvedL2, resolvedFaultDisputeGame, resolvedOutputRoot, _, err := l2.CreateGameProof(ctx, gameType, config, l1Header.ExecutionUpdate, results[0])
	if err != nil {
		return errors.WithStack(err)
	}
	trustedL2Num := big.NewInt(resolvedL2.Int64() + 1)

	consStateMPProof, err := l2.CreateMessagePasserAccountProof(ctx, config, trustedL2Num)
	if err != nil {
		return errors.WithStack(err)
	}
	resolvedMPProof, err := l2.CreateMessagePasserAccountProof(ctx, config, resolvedL2)
	if err != nil {
		return errors.WithStack(err)
	}

	faultyL2History, faultyOutput, err := createFaultyL2History(ctx, config, resolvedL2, trustedL2Num, consStateMPProof)
	if err != nil {
		return errors.WithStack(err)
	}

	misbehaviour := types.Misbehaviour{
		ClientId: "optimism-01",
		TrustedHeight: &types2.Height{
			RevisionNumber: 0,
			RevisionHeight: trustedL2Num.Uint64(),
		},
		LatestL1Header:                  l1Header,
		FirstL2ToL1MessagePasserAccount: consStateMPProof,
		LastL2ToL1MessagePasserAccount:  resolvedMPProof,
		ResolvedL2Number:                resolvedL2.Uint64(),
		ResolvedOutputRoot:              resolvedOutputRoot[:],
		L2HeaderHistory:                 faultyL2History,
		FaultDisputeGameFactoryProof:    resolvedFaultDisputeGame,
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
		OutputRoot:             faultyOutput,
		L1Slot:                 l1InitialState.Slot,
		L1CurrentSyncCommittee: l1InitialState.CurrentSyncCommittee.AggregatePubkey,
		L1NextSyncCommittee:    l1InitialState.NextSyncCommittee.AggregatePubkey,
		L1Timestamp:            l1InitialState.Timestamp,
		StorageRoot:            make([]byte, 32), // unused
	}

	misbehaviourBytes, err := clientMessage.Marshal()
	if err != nil {
		return errors.WithStack(err)
	}
	if err = os.WriteFile("submit_misbehaviour.bin", misbehaviourBytes, 0644); err != nil {
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

	honestL2History, honestOutput, err := createHonestL2History(ctx, config, resolvedL2, trustedL2Num, consStateMPProof)
	if err != nil {
		return errors.WithStack(err)
	}
	misbehaviour = types.Misbehaviour{
		ClientId: "optimism-01",
		TrustedHeight: &types2.Height{
			RevisionNumber: 0,
			RevisionHeight: trustedL2Num.Uint64(),
		},
		LatestL1Header:                  l1Header,
		FirstL2ToL1MessagePasserAccount: consStateMPProof,
		LastL2ToL1MessagePasserAccount:  resolvedMPProof,
		ResolvedL2Number:                resolvedL2.Uint64(),
		ResolvedOutputRoot:              resolvedOutputRoot[:],
		L2HeaderHistory:                 honestL2History,
		FaultDisputeGameFactoryProof:    resolvedFaultDisputeGame,
	}
	clientMessage, err = types2.PackClientMessage(&misbehaviour)
	if err != nil {
		return errors.WithStack(err)
	}
	misbehaviourBytes, err = clientMessage.Marshal()
	if err != nil {
		return errors.WithStack(err)
	}
	if err = os.WriteFile("submit_misbehaviour_not_misbehaviour.bin", misbehaviourBytes, 0644); err != nil {
		return errors.WithStack(err)
	}
	fmt.Printf("honest output root: %s\n", common.Bytes2Hex(honestOutput))
	return nil
}

func createFaultyL2History(ctx context.Context, config *l2.Config, resolvedNum *big.Int, trustedNum *big.Int, consStateMPProof *types.AccountUpdate) ([][]byte, []byte, error) {

	// Construct Faulty L2 History
	faultyL2HistoryRLPs := make([][]byte, 2)
	faultResolvedHeader, err := config.L2Client.HeaderByNumber(ctx, resolvedNum)
	if err != nil {
		return nil, nil, errors.WithStack(err)
	}
	faultResolvedHeader.Coinbase = common.Address{}
	faultyL2HistoryRLPs[1], err = rlp.EncodeToBytes(faultResolvedHeader)
	if err != nil {
		return nil, nil, errors.WithStack(err)
	}
	faultyTrustedHeader, err := config.L2Client.HeaderByNumber(ctx, trustedNum)
	if err != nil {
		return nil, nil, errors.WithStack(err)
	}
	faultyTrustedHeader.ParentHash = faultResolvedHeader.Hash()
	faultyL2HistoryRLPs[0], err = rlp.EncodeToBytes(faultyTrustedHeader)
	if err != nil {
		return nil, nil, errors.WithStack(err)
	}
	output := eth.OutputRoot(&eth.OutputV0{
		StateRoot:                eth.Bytes32(faultyTrustedHeader.Root),
		MessagePasserStorageRoot: eth.Bytes32(consStateMPProof.AccountStorageRoot),
		BlockHash:                faultyTrustedHeader.Hash(),
	})
	return faultyL2HistoryRLPs, output[:], nil
}

func createHonestL2History(ctx context.Context, config *l2.Config, resolvedNum *big.Int, trustedNum *big.Int, consStateMPProof *types.AccountUpdate) ([][]byte, []byte, error) {

	// Construct Faulty L2 History
	l2HistoryRLPs := make([][]byte, 2)
	resolvedHeader, err := config.L2Client.HeaderByNumber(ctx, resolvedNum)
	if err != nil {
		return nil, nil, errors.WithStack(err)
	}
	l2HistoryRLPs[1], err = rlp.EncodeToBytes(resolvedHeader)
	if err != nil {
		return nil, nil, errors.WithStack(err)
	}
	faultyTrustedHeader, err := config.L2Client.HeaderByNumber(ctx, trustedNum)
	if err != nil {
		return nil, nil, errors.WithStack(err)
	}
	faultyTrustedHeader.ParentHash = resolvedHeader.Hash()
	l2HistoryRLPs[0], err = rlp.EncodeToBytes(faultyTrustedHeader)
	if err != nil {
		return nil, nil, errors.WithStack(err)
	}
	output := eth.OutputRoot(&eth.OutputV0{
		StateRoot:                eth.Bytes32(faultyTrustedHeader.Root),
		MessagePasserStorageRoot: eth.Bytes32(consStateMPProof.AccountStorageRoot),
		BlockHash:                faultyTrustedHeader.Hash(),
	})
	return l2HistoryRLPs, output[:], nil
}
