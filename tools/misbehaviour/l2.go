package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/cockroachdb/errors"
	types2 "github.com/cosmos/ibc-go/v8/modules/core/02-client/types"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/prover/l1"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/types"
	bindings2 "github.com/ethereum-optimism/optimism/op-e2e/bindings"
	"github.com/ethereum-optimism/optimism/op-node/bindings"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/predeploys"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
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
	results, err := disputeGameFactoryCaller.FindLatestGames(nil, gameType, big.NewInt(start), big.NewInt(2))
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

	l1InitialState, err := proverL1Client.BuildInitialState(ctx, l1Header.ExecutionUpdate.BlockNumber+10)
	if err != nil {
		return errors.WithStack(err)
	}
	l1Config, err := proverL1Client.BuildL1Config(l1InitialState, 0, 86400*time.Second)
	if err != nil {
		return errors.WithStack(err)
	}

	// Get emulated trusted
	trustedL2, _, trustedOutputRoot, err := createGameProof(ctx, config, l1Header, results[0])
	if err != nil {
		return errors.WithStack(err)
	}
	// Get resolved (must be older than or equals to trusted)
	resolvedL2, resolvedFaultDisputeGame, resolvedOutputRoot, err := createGameProof(ctx, config, l1Header, results[1])
	if err != nil {
		return errors.WithStack(err)
	}
	headerRLPs := make([][]byte, 0)
	for i := trustedL2.Int64(); i >= resolvedL2.Int64(); i-- {
		l2, err := l2Client.HeaderByNumber(ctx, big.NewInt(i))
		if err != nil {
			return errors.WithStack(err)
		}
		buf := make([]byte, 0)
		if err = l2.EncodeRLP(bytes.NewBuffer(buf)); err != nil {
			return errors.WithStack(err)
		}
		headerRLPs = append(headerRLPs, buf)
	}

	misbehaviour := types.Misbehaviour{
		ClientId: "optimism-01",
		TrustedHeight: &types2.Height{
			RevisionNumber: 0,
			RevisionHeight: trustedL2.Uint64(),
		},
		TrustedOutput:                trustedOutputRoot,
		ResolvedOutput:               resolvedOutputRoot,
		TrustedToResolvedL2:          headerRLPs,
		FaultDisputeGameFactoryProof: resolvedFaultDisputeGame,
	}

	cs := &types.ClientState{
		LatestHeight: &types2.Height{
			RevisionNumber: 0,
			RevisionHeight: trustedL2.Uint64(),
		},
		L1Config: l1Config,
		FaultDisputeGameConfig: &types.FaultDisputeGameConfig{
			DisputeGameFactoryTargetStorageSlot: 103,
			FaultDisputeGameStatusSlot:          0,
			FaultDisputeGameStatusSlotOffset:    15,
		},
	}

	consState := types.ConsensusState{
		OutputRoot:             trustedOutputRoot.OutputRoot,
		L1Slot:                 l1InitialState.Slot,
		L1CurrentSyncCommittee: l1InitialState.CurrentSyncCommittee.AggregatePubkey,
		L1NextSyncCommittee:    l1InitialState.NextSyncCommittee.AggregatePubkey,
	}

	misbehaviourBytes, err := misbehaviour.Marshal()
	if err != nil {
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
	fmt.Printf("Misbehaviour: %s\n", common.Bytes2Hex(misbehaviourBytes))

	return nil
}

func createGameProof(
	ctx context.Context,
	config *Config,
	l1Header *types.L1Header,
	gameResult bindings.IDisputeGameFactoryGameSearchResult,
) (*big.Int, *types.FaultDisputeGameFactoryProof, *types.OutputRootWithMessagePasser, error) {
	gameId := gameResult.Metadata
	l2BlockNum := big.NewInt(0).SetBytes(gameResult.ExtraData)
	rootClaim := gameResult.RootClaim
	fmt.Printf("expected gameId=%s blockNum=%d, rootClaim=%s\n", common.Bytes2Hex(gameId[:]), l2BlockNum, common.Bytes2Hex(rootClaim[:]))

	// message passer
	fmt.Printf("Get message passer proof for L2ToL1MessagePasserAddr=%s at block %d\n", predeploys.L2ToL1MessagePasserAddr.String(), l2BlockNum)
	mpAccountProof, err := getProof(context.Background(), config.L2Client, predeploys.L2ToL1MessagePasserAddr, []common.Hash{}, fmt.Sprintf("0x%x", l2BlockNum))
	if err != nil {
		return nil, nil, nil, errors.WithStack(err)
	}
	mpAccountProofRLP, err := encodeRLP(mpAccountProof.AccountProof)
	if err != nil {
		return nil, nil, nil, errors.WithStack(err)
	}
	resolvedOutputRootWithMessagePasser := &types.OutputRootWithMessagePasser{
		OutputRoot: rootClaim[:],
		L2ToL1MessagePasserAccount: &types.AccountUpdate{
			AccountProof:       mpAccountProofRLP,
			AccountStorageRoot: mpAccountProof.StorageHash[:],
		},
	}

	// Get GameUUID
	gameUUID, err := config.DisputeGameFactoryCaller.GetGameUUID(nil, 0, rootClaim, l2BlockNum.Bytes())
	if err != nil {
		return nil, nil, nil, errors.WithStack(err)
	}
	slotForGameId := calculateMappingSlotBytes(gameUUID[:], uint64(103))
	fmt.Printf("gameUUID=%s, slotForGameId %v\n", common.Bytes2Hex(gameUUID[:]), slotForGameId.String())

	// Get Proof of DisputeGameFactoryProxy.sol
	disputeGameFactoryAccountProof, err := getProof(ctx, config.L1Client, config.DisputeGameFactoryAddress, []common.Hash{
		slotForGameId,
	}, fmt.Sprintf("0x%x", l1Header.ExecutionUpdate.BlockNumber))
	if err != nil {
		return nil, nil, nil, errors.WithStack(err)
	}
	accountProofRLP, err := encodeRLP(disputeGameFactoryAccountProof.AccountProof)
	if err != nil {
		return nil, nil, nil, errors.WithStack(err)
	}
	storageProofRLP, err := encodeRLP(disputeGameFactoryAccountProof.StorageProof[0].Proof)
	if err != nil {
		return nil, nil, nil, errors.WithStack(err)
	}

	// Get Proof of FaultDisputeGame.sol
	gameType, timestamp, gameAddress := unpackGameId(gameId)
	faultDisputeGameCaller, err := bindings2.NewFaultDisputeGameCaller(gameAddress, config.L1Client)
	if err != nil {
		return nil, nil, nil, errors.WithStack(err)
	}
	status, err := faultDisputeGameCaller.Status(nil)
	if err != nil {
		return nil, nil, nil, errors.WithStack(err)
	}
	fmt.Printf("gameType=%d, timestamp=%d, gameAddress=%s, status=%d\n", gameType, timestamp, gameAddress, status)
	time.Sleep(10 * time.Second)
	faultDisputeGameProof, err := getProof(context.Background(), config.L1Client, gameAddress, []common.Hash{
		common.BigToHash(big.NewInt(0)),
	}, fmt.Sprintf("0x%x", l1Header.ExecutionUpdate.BlockNumber))
	if err != nil {
		return nil, nil, nil, errors.WithStack(err)
	}
	faultDisputeGameAccountProofRLP, err := encodeRLP(faultDisputeGameProof.AccountProof)
	if err != nil {
		return nil, nil, nil, errors.WithStack(err)
	}
	faultDisputeGameStorageRLP, err := encodeRLP(faultDisputeGameProof.StorageProof[0].Proof)
	if err != nil {
		return nil, nil, nil, errors.WithStack(err)
	}

	disputeGameFactoryProof := types.FaultDisputeGameFactoryProof{
		L1Header:                  l1Header,
		DisputeGameFactoryAddress: config.DisputeGameFactoryAddress.Bytes(),
		DisputeGameFactoryAccount: &types.AccountUpdate{
			AccountProof:       accountProofRLP,
			AccountStorageRoot: disputeGameFactoryAccountProof.StorageHash.Bytes(),
		},
		DisputeGameFactoryStorageProof: storageProofRLP,
		FaultDisputeGameAccount: &types.AccountUpdate{
			AccountProof:       faultDisputeGameAccountProofRLP,
			AccountStorageRoot: faultDisputeGameProof.StorageHash.Bytes(),
		},
		FaultDisputeGameStorageProof:   faultDisputeGameStorageRLP,
		FaultDisputeGameSourceGameType: gameType,
	}

	return l2BlockNum, &disputeGameFactoryProof, resolvedOutputRootWithMessagePasser, nil
}

func calculateMappingSlotBytes(keyBytes []byte, mappingSlot uint64) common.Hash {
	mappingSlotBytes := common.LeftPadBytes(big.NewInt(int64(mappingSlot)).Bytes(), 32)

	// Concatenate key and mapping slot
	concatenated := append(keyBytes, mappingSlotBytes...)

	// Calculate the keccak256 hash
	slotHash := crypto.Keccak256(concatenated)
	return common.BytesToHash(slotHash[:])
}

func getProof(ctx context.Context, client *ethclient.Client, address common.Address, storage []common.Hash, blockTag string) (*eth.AccountResult, error) {
	var getProofResponse *eth.AccountResult
	err := client.Client().CallContext(ctx, &getProofResponse, "eth_getProof", address, storage, blockTag)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	if getProofResponse == nil {
		return nil, errors.WithStack(err)
	}
	if len(getProofResponse.StorageProof) != len(storage) {
		return nil, errors.WithStack(fmt.Errorf("missing storage proof data, got %d proof entries but requested %d storage keys", len(getProofResponse.StorageProof), len(storage)))
	}
	for i, key := range storage {
		if key.String() != getProofResponse.StorageProof[i].Key.String() {
			return nil, errors.WithStack(fmt.Errorf("unexpected storage proof key difference for entry %d: got %s but requested %s", i, getProofResponse.StorageProof[i].Key, key))
		}
	}
	return getProofResponse, nil
}

func encodeRLP(proof []hexutil.Bytes) ([]byte, error) {
	target := make([][]byte, len(proof))
	for i, p := range proof {
		target[i] = p
	}
	return rlp.EncodeToBytes(target)
}

func unpackGameId(gameId [32]byte) (uint64, uint64, common.Address) {
	gameType := big.NewInt(0).SetBytes(gameId[0:4]).Uint64()
	timestamp := big.NewInt(0).SetBytes(gameId[4:12]).Uint64()
	gameAddress := gameId[12:]
	return gameType, timestamp, common.BytesToAddress(gameAddress)
}
