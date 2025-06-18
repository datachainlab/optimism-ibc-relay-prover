package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/cockroachdb/errors"
	types2 "github.com/cosmos/ibc-go/v8/modules/core/02-client/types"
	"github.com/datachainlab/ethereum-ibc-relay-chain/pkg/client"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/prover/l1"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/types"
	bindings2 "github.com/ethereum-optimism/optimism/op-e2e/bindings"
	"github.com/ethereum-optimism/optimism/op-node/bindings"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/predeploys"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
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

type HostPort struct {
	L1BeaconPort int `json:"l1BeaconPort"`
	L1GethPort   int `json:"l1GethPort"`
	L2GethPort   int `json:"l2GethPort"`
}

type Config struct {
	ProverL1Client            *l1.L1Client
	L1Client                  *ethclient.Client
	L2Client                  *ethclient.Client
	DisputeGameFactoryCaller  *bindings.DisputeGameFactoryCaller
	DisputeGameFactoryAddress common.Address
}

func run(ctx context.Context) error {
	hostPortJson, err := os.ReadFile("../hostPort.json")
	if err != nil {
		return errors.WithStack(err)
	}
	var hostPort HostPort
	if err = json.Unmarshal(hostPortJson, &hostPort); err != nil {
		return errors.WithStack(err)
	}
	disputeGameFactoryProxyAddr := common.HexToAddress("0x41569d8c2612a380c4fdc425845246c41bc4f4ad")
	executionNode := fmt.Sprintf("http://localhost:%d", hostPort.L1GethPort)
	l1Client, err := ethclient.Dial(executionNode)
	if err != nil {
		return errors.WithStack(err)
	}
	l2Client, err := ethclient.Dial(fmt.Sprintf("http://localhost:%d", hostPort.L2GethPort))
	if err != nil {
		return errors.WithStack(err)
	}
	proverL1Client, err := l1.NewL1Client(ctx,
		fmt.Sprintf("http://localhost:%d", hostPort.L1BeaconPort),
		executionNode,
		log.GetLogger().WithModule("l1"),
	)
	if err != nil {
		return errors.WithStack(err)
	}
	disputeGameFactoryCaller, err := bindings.NewDisputeGameFactoryCaller(disputeGameFactoryProxyAddr, l1Client)
	if err != nil {
		return errors.WithStack(err)
	}
	config := &Config{
		ProverL1Client:            proverL1Client,
		L1Client:                  l1Client,
		L2Client:                  l2Client,
		DisputeGameFactoryCaller:  disputeGameFactoryCaller,
		DisputeGameFactoryAddress: disputeGameFactoryProxyAddr,
	}

	// Find latest game
	gameCount, err := disputeGameFactoryCaller.GameCount(nil)
	if err != nil {
		return errors.WithStack(err)
	}
	start := gameCount.Int64() - 5
	gameType := uint32(1) // Permission Cannon in local net
	results, err := disputeGameFactoryCaller.FindLatestGames(nil, gameType, big.NewInt(start), big.NewInt(1))
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

	l1InitialState, err := proverL1Client.BuildInitialState(ctx, l1Header.ExecutionUpdate.BlockNumber)
	if err != nil {
		return errors.WithStack(err)
	}
	l1Config, err := proverL1Client.BuildL1Config(l1InitialState, 0, 86400*time.Second)
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
	resolvedL2, resolvedFaultDisputeGame, resolvedOutputRoot, err := createGameProof(ctx, gameType, config, l1Header, results[0])
	if err != nil {
		return errors.WithStack(err)
	}
	trustedL2Num := big.NewInt(resolvedL2.Int64() + 1)

	consStateMPProof, err := createMessagePasserAccountProof(ctx, config, trustedL2Num)
	if err != nil {
		return errors.WithStack(err)
	}
	resolvedMPProof, err := createMessagePasserAccountProof(ctx, config, resolvedL2)
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
		FirstL2ToL1MessagePasserAccount: consStateMPProof,
		LastL2ToL1MessagePasserAccount:  resolvedMPProof,
		ResolvedOutputRoot:              resolvedOutputRoot[:],
		L2HeaderHistory:                 faultyL2History,
		FaultDisputeGameFactoryProof:    resolvedFaultDisputeGame,
	}
	clientMessage, err := types2.PackClientMessage(&misbehaviour)
	if err != nil {
		return errors.WithStack(err)
	}

	cs := &types.ClientState{
		LatestHeight: misbehaviour.TrustedHeight,
		L1Config:     l1Config,
		FaultDisputeGameConfig: &types.FaultDisputeGameConfig{
			DisputeGameFactoryTargetStorageSlot: 103,
			FaultDisputeGameStatusSlot:          0,
			FaultDisputeGameStatusSlotOffset:    15,
			StatusDefenderWin:                   2,
		},
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

	return nil
}

func createFaultyL2History(ctx context.Context, config *Config, resolvedNum *big.Int, trustedNum *big.Int, consStateMPProof *types.AccountUpdate) ([][]byte, []byte, error) {

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

func createMessagePasserAccountProof(ctx context.Context, config *Config, l2BlockNum *big.Int) (*types.AccountUpdate, error) {
	l2ProofGetter, err := client.NewETHClientWith(config.L2Client)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	address := predeploys.L2ToL1MessagePasserAddr
	fmt.Printf("Get message passer proof for address=%s at block %d\n", address.String(), l2BlockNum)
	mpAccountProof, err := l2ProofGetter.GetProof(ctx, address, nil, l2BlockNum)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return &types.AccountUpdate{
		AccountProof:       mpAccountProof.AccountProofRLP,
		AccountStorageRoot: mpAccountProof.StorageHash[:],
	}, nil
}

func createGameProof(
	ctx context.Context,
	targetGameType uint32,
	config *Config,
	l1Header *types.L1Header,
	gameResult bindings.IDisputeGameFactoryGameSearchResult,
) (*big.Int, *types.FaultDisputeGameFactoryProof, [32]byte, error) {
	gameId := gameResult.Metadata
	l2BlockNum := big.NewInt(0).SetBytes(gameResult.ExtraData)
	rootClaim := gameResult.RootClaim
	fmt.Printf("expected gameId=%s blockNum=%d, rootClaim=%s\n", common.Bytes2Hex(gameId[:]), l2BlockNum, common.Bytes2Hex(rootClaim[:]))

	// Get GameUUID
	gameUUID, err := config.DisputeGameFactoryCaller.GetGameUUID(nil, targetGameType, rootClaim, common.BytesToHash(l2BlockNum.Bytes()).Bytes())
	if err != nil {
		return nil, nil, rootClaim, errors.WithStack(err)
	}
	slotForGameId := calculateMappingSlotBytes(gameUUID[:], uint64(103))
	fmt.Printf("gameUUID=%s, slotForGameId %v\n", common.Bytes2Hex(gameUUID[:]), slotForGameId.String())

	l1ProofGetter, err := client.NewETHClientWith(config.L1Client)
	if err != nil {
		return nil, nil, rootClaim, errors.WithStack(err)
	}
	// Get Proof of DisputeGameFactoryProxy.sol
	marshallSlotForGameId, err := slotForGameId.MarshalText()
	if err != nil {
		return nil, nil, rootClaim, errors.WithStack(err)
	}
	disputeGameFactoryAccountProof, err := l1ProofGetter.GetProof(ctx,
		config.DisputeGameFactoryAddress,
		[][]byte{marshallSlotForGameId},
		big.NewInt(0).SetUint64(l1Header.ExecutionUpdate.BlockNumber))
	if err != nil {
		return nil, nil, rootClaim, errors.WithStack(err)
	}

	// Get Proof of FaultDisputeGame.sol
	gameType, timestamp, gameAddress := unpackGameId(gameId)
	faultDisputeGameCaller, err := bindings2.NewFaultDisputeGameCaller(gameAddress, config.L1Client)
	if err != nil {
		return nil, nil, rootClaim, errors.WithStack(err)
	}
	status, err := faultDisputeGameCaller.Status(nil)
	if err != nil {
		return nil, nil, rootClaim, errors.WithStack(err)
	}
	fmt.Printf("gameType=%d, timestamp=%d, gameAddress=%s, status=%d\n", gameType, timestamp, gameAddress, status)
	time.Sleep(10 * time.Second)
	marshalSlotForStatus, err := common.BigToHash(big.NewInt(0)).MarshalText()
	if err != nil {
		return nil, nil, rootClaim, errors.WithStack(err)
	}
	faultDisputeGameProof, err := l1ProofGetter.GetProof(ctx, gameAddress,
		[][]byte{marshalSlotForStatus},
		big.NewInt(0).SetUint64(l1Header.ExecutionUpdate.BlockNumber),
	)
	if err != nil {
		return nil, nil, rootClaim, errors.WithStack(err)
	}

	disputeGameFactoryProof := types.FaultDisputeGameFactoryProof{
		L1Header:                  l1Header,
		DisputeGameFactoryAddress: config.DisputeGameFactoryAddress.Bytes(),
		DisputeGameFactoryAccount: &types.AccountUpdate{
			AccountProof:       disputeGameFactoryAccountProof.AccountProofRLP,
			AccountStorageRoot: disputeGameFactoryAccountProof.StorageHash[:],
		},
		DisputeGameFactoryStorageProof: disputeGameFactoryAccountProof.StorageProofRLP[0],
		FaultDisputeGameAccount: &types.AccountUpdate{
			AccountProof:       faultDisputeGameProof.AccountProofRLP,
			AccountStorageRoot: faultDisputeGameProof.StorageHash[:],
		},
		FaultDisputeGameStorageProof:   faultDisputeGameProof.StorageProofRLP[0],
		FaultDisputeGameSourceGameType: gameType,
	}

	return l2BlockNum, &disputeGameFactoryProof, rootClaim, nil
}

func calculateMappingSlotBytes(keyBytes []byte, mappingSlot uint64) common.Hash {
	mappingSlotBytes := common.LeftPadBytes(big.NewInt(int64(mappingSlot)).Bytes(), 32)

	// Concatenate key and mapping slot
	concatenated := append(keyBytes, mappingSlotBytes...)

	// Calculate the keccak256 hash
	slotHash := crypto.Keccak256(concatenated)
	return common.BytesToHash(slotHash[:])
}

func unpackGameId(gameId [32]byte) (uint64, uint64, common.Address) {
	gameType := big.NewInt(0).SetBytes(gameId[0:4]).Uint64()
	timestamp := big.NewInt(0).SetBytes(gameId[4:12]).Uint64()
	gameAddress := gameId[12:]
	return gameType, timestamp, common.BytesToAddress(gameAddress)
}
