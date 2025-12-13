package l2

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/datachainlab/ethereum-ibc-relay-chain/pkg/client"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/prover/l1"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/prover/l2"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/types"
	bindings2 "github.com/ethereum-optimism/optimism/op-e2e/bindings"
	"github.com/ethereum-optimism/optimism/op-node/bindings"
	"github.com/ethereum-optimism/optimism/op-service/predeploys"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/hyperledger-labs/yui-relayer/log"
)

type HostPort struct {
	L1BeaconPort int `json:"l1BeaconPort"`
	L1GethPort   int `json:"l1GethPort"`
	L2GethPort   int `json:"l2GethPort"`
}

type Config struct {
	ProverL1Client            *l1.L1Client
	ProverL2Client            *l2.L2Client
	L1Client                  *ethclient.Client
	L2Client                  *ethclient.Client
	DisputeGameFactoryCaller  *bindings.DisputeGameFactoryCaller
	DisputeGameFactoryAddress common.Address
}

func NewConfig(ctx context.Context) (*Config, error) {
	hostPortJson, err := os.ReadFile("../hostPort.json")
	if err != nil {
		return nil, errors.WithStack(err)
	}
	var hostPort HostPort
	if err = json.Unmarshal(hostPortJson, &hostPort); err != nil {
		return nil, errors.WithStack(err)
	}
	// see devnet configuration
	disputeGameFactoryProxyAddr := common.HexToAddress(os.Getenv("DISPUTE_GAME_FACTORY_ADDRESS_PROXY"))

	executionNode := fmt.Sprintf("http://localhost:%d", hostPort.L1GethPort)
	l1Client, err := ethclient.Dial(executionNode)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	l2Client, err := ethclient.Dial(fmt.Sprintf("http://localhost:%d", hostPort.L2GethPort))
	if err != nil {
		return nil, errors.WithStack(err)
	}
	proverL1Client, err := l1.NewL1Client(ctx,
		fmt.Sprintf("http://localhost:%d", hostPort.L1BeaconPort),
		executionNode,
		10*time.Second,
		"http://localhost:10080",
		nil,
		log.GetLogger().WithModule("l1"),
	)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	proverL2Client := l2.NewL2Client(nil, 10*time.Second,
		10*time.Second,
		"http://localhost:10080",
		"",
		log.GetLogger().WithModule("l1"),
	)
	disputeGameFactoryCaller, err := bindings.NewDisputeGameFactoryCaller(disputeGameFactoryProxyAddr, l1Client)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	config := &Config{
		ProverL1Client:            proverL1Client,
		ProverL2Client:            proverL2Client,
		L1Client:                  l1Client,
		L2Client:                  l2Client,
		DisputeGameFactoryCaller:  disputeGameFactoryCaller,
		DisputeGameFactoryAddress: disputeGameFactoryProxyAddr,
	}
	return config, nil
}

func (config *Config) ToFaultDisputeGameConfig() *types.FaultDisputeGameConfig {
	return &types.FaultDisputeGameConfig{
		DisputeGameFactoryAddress:           config.DisputeGameFactoryAddress.Bytes(),
		DisputeGameFactoryTargetStorageSlot: 103,
		FaultDisputeGameStatusSlot:          0,
		FaultDisputeGameStatusSlotOffset:    15,
		FaultDisputeGameCreatedAtSlotOffset: 24,
		StatusDefenderWin:                   2,
	}
}

func CreateMessagePasserAccountProof(ctx context.Context, config *Config, l2BlockNum *big.Int) (*types.AccountUpdate, error) {
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

func CalculateMappingSlotBytes(keyBytes []byte, mappingSlot uint64) common.Hash {
	mappingSlotBytes := common.LeftPadBytes(big.NewInt(int64(mappingSlot)).Bytes(), 32)

	// Concatenate key and mapping slot
	concatenated := append(keyBytes, mappingSlotBytes...)

	// Calculate the keccak256 hash
	slotHash := crypto.Keccak256(concatenated)
	return common.BytesToHash(slotHash[:])
}

func UnpackGameId(gameId [32]byte) (uint64, uint64, common.Address) {
	gameType := big.NewInt(0).SetBytes(gameId[0:4]).Uint64()
	timestamp := big.NewInt(0).SetBytes(gameId[4:12]).Uint64()
	gameAddress := gameId[12:]
	return gameType, timestamp, common.BytesToAddress(gameAddress)
}

func CreateGameProof(
	ctx context.Context,
	targetGameType uint32,
	config *Config,
	l1Header *types.ExecutionUpdate,
	gameResult bindings.IDisputeGameFactoryGameSearchResult,
) (*big.Int, *types.FaultDisputeGameProof, [32]byte, *bindings2.FaultDisputeGameCaller, error) {
	gameId := gameResult.Metadata
	l2BlockNum := big.NewInt(0).SetBytes(gameResult.ExtraData)
	rootClaim := gameResult.RootClaim
	fmt.Printf("expected gameId=%s blockNum=%d, rootClaim=%s\n", common.Bytes2Hex(gameId[:]), l2BlockNum, common.Bytes2Hex(rootClaim[:]))

	// Get GameUUID
	gameUUID, err := config.DisputeGameFactoryCaller.GetGameUUID(nil, targetGameType, rootClaim, common.BytesToHash(l2BlockNum.Bytes()).Bytes())
	if err != nil {
		return nil, nil, rootClaim, nil, errors.WithStack(err)
	}
	slotForGameId := CalculateMappingSlotBytes(gameUUID[:], uint64(103))
	fmt.Printf("gameUUID=%s, slotForGameId %v\n", common.Bytes2Hex(gameUUID[:]), slotForGameId.String())

	l1ProofGetter, err := client.NewETHClientWith(config.L1Client)
	if err != nil {
		return nil, nil, rootClaim, nil, errors.WithStack(err)
	}
	// Get Proof of DisputeGameFactoryProxy.sol
	marshallSlotForGameId, err := slotForGameId.MarshalText()
	if err != nil {
		return nil, nil, rootClaim, nil, errors.WithStack(err)
	}
	disputeGameFactoryAccountProof, err := l1ProofGetter.GetProof(ctx,
		config.DisputeGameFactoryAddress,
		[][]byte{marshallSlotForGameId},
		big.NewInt(0).SetUint64(l1Header.BlockNumber))
	if err != nil {
		return nil, nil, rootClaim, nil, errors.WithStack(err)
	}

	// Get Proof of FaultDisputeGame.sol
	gameType, timestamp, gameAddress := UnpackGameId(gameId)
	faultDisputeGameCaller, err := bindings2.NewFaultDisputeGameCaller(gameAddress, config.L1Client)
	if err != nil {
		return nil, nil, rootClaim, nil, errors.WithStack(err)
	}
	status, err := faultDisputeGameCaller.Status(nil)
	if err != nil {
		return nil, nil, rootClaim, nil, errors.WithStack(err)
	}
	fmt.Printf("gameType=%d, timestamp=%d, gameAddress=%s, status=%d\n", gameType, timestamp, gameAddress, status)
	marshalSlotForStatus, err := common.BigToHash(big.NewInt(0)).MarshalText()
	if err != nil {
		return nil, nil, rootClaim, nil, errors.WithStack(err)
	}
	faultDisputeGameProof, err := l1ProofGetter.GetProof(ctx, gameAddress,
		[][]byte{marshalSlotForStatus},
		big.NewInt(0).SetUint64(l1Header.BlockNumber),
	)
	if err != nil {
		return nil, nil, rootClaim, nil, errors.WithStack(err)
	}

	disputeGameFactoryProof := types.FaultDisputeGameProof{
		StateRoot: l1Header.StateRoot,
		DisputeGameFactoryAccount: &types.AccountUpdate{
			AccountProof:       disputeGameFactoryAccountProof.AccountProofRLP,
			AccountStorageRoot: disputeGameFactoryAccountProof.StorageHash[:],
		},
		DisputeGameFactoryGameIdProof: disputeGameFactoryAccountProof.StorageProofRLP[0],
		FaultDisputeGameAccount: &types.AccountUpdate{
			AccountProof:       faultDisputeGameProof.AccountProofRLP,
			AccountStorageRoot: faultDisputeGameProof.StorageHash[:],
		},
		FaultDisputeGameGameStatusProof: faultDisputeGameProof.StorageProofRLP[0],
		FaultDisputeGameSourceGameType:  gameType,
	}

	return l2BlockNum, &disputeGameFactoryProof, rootClaim, faultDisputeGameCaller, nil
}
