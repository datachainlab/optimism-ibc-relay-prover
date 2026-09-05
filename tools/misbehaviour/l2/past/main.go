package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/cockroachdb/errors"
	types2 "github.com/cosmos/ibc-go/v8/modules/core/02-client/types"
	lcrelay "github.com/datachainlab/ethereum-light-client-types/prover/relay"
	lctypes "github.com/datachainlab/ethereum-light-client-types/prover/types"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/types"
	"github.com/datachainlab/optimism-ibc-relay-prover/tools/misbehaviour/l2"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/hyperledger-labs/yui-relayer/log"
)

func main() {
	_ = log.InitLogger("debug", "text", "stdout", false)
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
	start := gameCount.Int64() - 2
	if start < 0 {
		return errors.Errorf("Insufficient games start=%d", start)
	}
	gameType, err := l2.GameType()
	if err != nil {
		return errors.WithStack(err)
	}
	results, err := config.DisputeGameFactoryCaller.FindLatestGames(nil, gameType, big.NewInt(start), big.NewInt(1))
	if err != nil {
		return errors.WithStack(err)
	}
	if len(results) == 0 {
		return errors.Errorf("no game found for gameType=%d, start=%d", gameType, start)
	}

	// Get finalized L1
	latestMetadata, err := config.ProverL2Client.GetLatestPreimageMetadata(ctx)
	if err != nil {
		return errors.WithStack(err)
	}
	l1Header, _, lcUpdateSnapshot, err := config.ProverL1Client.GetFinalizedL1Header(ctx, latestMetadata.L1Head)
	if err != nil {
		return errors.WithStack(err)
	}
	// The LatestL1Header must be self-consistent: ConsensusUpdate, ExecutionUpdate
	// and Timestamp must all describe the same finalized header. The snapshot update
	// carries the next_sync_committee needed for L1 verification, so build all three
	// from the snapshot's finalized header (GetFinalizedL1Header builds the execution
	// update / timestamp from the finality update, whose finalized header may differ).
	l1Header.ConsensusUpdate = lcUpdateSnapshot.ToProto()
	snapshotExecutionUpdate, snapshotTimestamp, err := lcrelay.BuildExecutionUpdateFromFinalizedHeader(ctx, config.L1Client.Client(), &lcUpdateSnapshot.FinalizedHeader, true)
	if err != nil {
		return errors.WithStack(err)
	}
	l1Header.ExecutionUpdate = snapshotExecutionUpdate
	l1Header.Timestamp = snapshotTimestamp
	fmt.Printf("l1 state root=%s\n", common.Bytes2Hex(l1Header.ExecutionUpdate.StateRoot))

	l1InitialState, err := config.ProverL1Client.BuildInitialState(ctx, l1Header.ExecutionUpdate.BlockNumber)
	if err != nil {
		return errors.WithStack(err)
	}
	l1Config, err := config.ProverL1Client.BuildL1Config(l1InitialState, 0, 86400*time.Second)
	if err != nil {
		return errors.WithStack(err)
	}
	l1Header.TrustedSyncCommittee = &lctypes.TrustedSyncCommittee{
		// L1 verification ignores the height, but TrustedSyncCommittee::validate
		// requires it to be present with revision_number == 0.
		TrustedHeight: &types2.Height{RevisionNumber: 0, RevisionHeight: 0},
		IsNext:        true,
		SyncCommittee: &lctypes.SyncCommittee{
			Pubkeys:         l1InitialState.NextSyncCommittee.Pubkeys,
			AggregatePubkey: l1InitialState.NextSyncCommittee.AggregatePubkey,
		},
	}

	// Get resolved
	superRoot, resolvedFaultDisputeGame, _, err := l2.CreateGameProof(ctx, gameType, config, l1Header.ExecutionUpdate, results[0])
	if err != nil {
		return errors.WithStack(err)
	}
	// A Super Root game identifies its proposal by timestamp, so resolve it back to the
	// L2 block the light client's header history has to end at.
	resolvedL2, err := l2.FindL2BlockByTimestamp(ctx, config.L2Client, superRoot.Timestamp)
	if err != nil {
		return errors.WithStack(err)
	}
	l2ChainID, err := config.L2Client.ChainID(ctx)
	if err != nil {
		return errors.WithStack(err)
	}
	resolvedOutputRoot, err := superRoot.OutputRootOf(l2ChainID)
	if err != nil {
		return errors.WithStack(err)
	}
	fmt.Printf("resolved l2=%d timestamp=%d outputRoot=%s\n", resolvedL2, superRoot.Timestamp, resolvedOutputRoot.String())
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
		SuperRootProof:                  superRoot.Raw,
		L2HeaderHistory:                 faultyL2History,
		FaultDisputeGameProof:           resolvedFaultDisputeGame,
	}
	clientMessage, err := types2.PackClientMessage(&misbehaviour)
	if err != nil {
		return errors.WithStack(err)
	}

	cs := &types.ClientState{
		// the light client picks this chain's output root out of the super root proof by chain id
		ChainId:                l2ChainID.Uint64(),
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

	// The optimism-elc e2e test reads the client message from the .bin and the
	// initial state (client_state / consensus_state / now) from the .json.
	output := struct {
		Now            int64  `json:"now"`
		ClientState    string `json:"client_state"`
		ConsensusState string `json:"consensus_state"`
	}{
		Now:            time.Now().Unix(),
		ClientState:    common.Bytes2Hex(csBytes),
		ConsensusState: common.Bytes2Hex(consStateBytes),
	}
	encodedOutput, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return errors.WithStack(err)
	}
	if err = os.WriteFile("submit_misbehaviour.json", encodedOutput, 0644); err != nil {
		return errors.WithStack(err)
	}

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
		SuperRootProof:                  superRoot.Raw,
		L2HeaderHistory:                 honestL2History,
		FaultDisputeGameProof:           resolvedFaultDisputeGame,
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

	// The honest history only contradicts nothing if the trusted consensus state is the one
	// it starts from, so the not-misbehaviour case needs its own initial state.
	consState.OutputRoot = honestOutput
	notMisbehaviourConsStateBytes, err := consState.Marshal()
	if err != nil {
		return errors.WithStack(err)
	}
	output.ConsensusState = common.Bytes2Hex(notMisbehaviourConsStateBytes)
	encodedOutput, err = json.MarshalIndent(output, "", "  ")
	if err != nil {
		return errors.WithStack(err)
	}
	if err = os.WriteFile("submit_misbehaviour_not_misbehaviour.json", encodedOutput, 0644); err != nil {
		return errors.WithStack(err)
	}
	return nil
}

func createFaultyL2History(ctx context.Context, config *l2.Config, resolvedNum *big.Int, trustedNum *big.Int, consStateMPProof *lctypes.AccountUpdate) ([][]byte, []byte, error) {

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

func createHonestL2History(ctx context.Context, config *l2.Config, resolvedNum *big.Int, trustedNum *big.Int, consStateMPProof *lctypes.AccountUpdate) ([][]byte, []byte, error) {

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
