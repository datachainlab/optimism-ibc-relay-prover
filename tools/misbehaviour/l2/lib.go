package l2

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/datachainlab/ethereum-ibc-relay-chain/pkg/client"
	lctypes "github.com/datachainlab/ethereum-light-client-types/prover/types"
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
		log.GetLogger().WithModule("l2"),
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

func CreateMessagePasserAccountProof(ctx context.Context, config *Config, l2BlockNum *big.Int) (*lctypes.AccountUpdate, error) {
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
	return &lctypes.AccountUpdate{
		AccountProof:       mpAccountProof.AccountProofRLP,
		AccountStorageRoot: mpAccountProof.StorageHash[:],
	}, nil
}

// SuperPermissionedGameType is the respected game type of an Upgrade 20 chain that
// runs permissioned proofs. op-deployer's standard.DisputeGameType defaults to it.
const SuperPermissionedGameType = uint32(5)

// GameType returns the dispute game type to search for, overridable for chains whose
// respected game type is not SUPER_PERMISSIONED (e.g. 9 = SUPER_CANNON_KONA).
func GameType() (uint32, error) {
	raw := os.Getenv("DISPUTE_GAME_TYPE")
	if raw == "" {
		return SuperPermissionedGameType, nil
	}
	parsed, err := strconv.ParseUint(raw, 10, 32)
	if err != nil {
		return 0, errors.Wrapf(err, "invalid DISPUTE_GAME_TYPE: %s", raw)
	}
	return uint32(parsed), nil
}

const (
	superRootProofVersion    = 0x01
	superRootProofHeaderSize = 1 + 8   // version + timestamp
	superRootProofEntrySize  = 32 + 32 // chainId + outputRoot
)

type SuperRootEntry struct {
	ChainID    *big.Int
	OutputRoot common.Hash
}

// SuperRoot is the decoded preimage of a Super Root game's root claim, which the game
// carries as its extraData:
//
//	version(1) || timestamp(8) || (chainId(32) || outputRoot(32))*n
//
// See Encoding.encodeSuperRootProof in contracts-bedrock.
type SuperRoot struct {
	Raw       []byte
	RootClaim common.Hash
	Version   byte
	Timestamp uint64
	Entries   []SuperRootEntry
}

func DecodeSuperRootProof(raw []byte) (*SuperRoot, error) {
	if len(raw) < superRootProofHeaderSize {
		return nil, errors.Errorf("super root proof is too short: size=%d", len(raw))
	}
	if raw[0] != superRootProofVersion {
		return nil, errors.Errorf("unexpected super root proof version: version=%d", raw[0])
	}
	entries := len(raw) - superRootProofHeaderSize
	if entries == 0 || entries%superRootProofEntrySize != 0 {
		return nil, errors.Errorf("unexpected super root proof size: size=%d", len(raw))
	}
	superRoot := &SuperRoot{
		Raw:       raw,
		RootClaim: crypto.Keccak256Hash(raw),
		Version:   raw[0],
		Timestamp: binary.BigEndian.Uint64(raw[1:superRootProofHeaderSize]),
	}
	for offset := superRootProofHeaderSize; offset < len(raw); offset += superRootProofEntrySize {
		superRoot.Entries = append(superRoot.Entries, SuperRootEntry{
			ChainID:    new(big.Int).SetBytes(raw[offset : offset+32]),
			OutputRoot: common.BytesToHash(raw[offset+32 : offset+superRootProofEntrySize]),
		})
	}
	return superRoot, nil
}

// OutputRootOf returns the output root this super root commits to for chainID, the
// equivalent of SuperFaultDisputeGame.rootClaimByChainId.
func (s *SuperRoot) OutputRootOf(chainID *big.Int) (common.Hash, error) {
	for _, entry := range s.Entries {
		if entry.ChainID.Cmp(chainID) == 0 {
			return entry.OutputRoot, nil
		}
	}
	return common.Hash{}, errors.Errorf("chain id not found in super root proof: chainId=%s", chainID)
}

// FindL2BlockByTimestamp maps a Super Root game's l2SequenceNumber, which is a
// timestamp, back to the L2 block the light client has to end its header history at.
func FindL2BlockByTimestamp(ctx context.Context, l2Client *ethclient.Client, timestamp uint64) (*big.Int, error) {
	latest, err := l2Client.HeaderByNumber(ctx, nil)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	if latest.Time < timestamp {
		return nil, errors.Errorf("timestamp is ahead of the l2 chain: timestamp=%d latest=%d", timestamp, latest.Time)
	}
	blockTime := uint64(1)
	if latest.Number.Sign() > 0 {
		previous, err := l2Client.HeaderByNumber(ctx, new(big.Int).Sub(latest.Number, big.NewInt(1)))
		if err != nil {
			return nil, errors.WithStack(err)
		}
		if latest.Time > previous.Time {
			blockTime = latest.Time - previous.Time
		}
	}
	candidate := new(big.Int).Sub(latest.Number, new(big.Int).SetUint64((latest.Time-timestamp)/blockTime))
	for i := 0; i < 1024; i++ {
		if candidate.Sign() < 0 {
			break
		}
		header, err := l2Client.HeaderByNumber(ctx, candidate)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		if header.Time == timestamp {
			return candidate, nil
		}
		if header.Time > timestamp {
			candidate = new(big.Int).Sub(candidate, big.NewInt(1))
		} else {
			candidate = new(big.Int).Add(candidate, big.NewInt(1))
		}
	}
	return nil, errors.Errorf("no l2 block found for timestamp=%d", timestamp)
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
	l1Header *lctypes.ExecutionUpdate,
	gameResult bindings.IDisputeGameFactoryGameSearchResult,
) (*SuperRoot, *types.FaultDisputeGameProof, *bindings2.FaultDisputeGameCaller, error) {
	gameId := gameResult.Metadata
	rootClaim := gameResult.RootClaim

	// A Super Root game's extraData is the encoded SuperRootProof, and its
	// initialize() reverts unless keccak256(extraData) == rootClaim, so the light client
	// derives the root claim from the preimage. Fail early if they disagree.
	superRoot, err := DecodeSuperRootProof(gameResult.ExtraData)
	if err != nil {
		return nil, nil, nil, err
	}
	if superRoot.RootClaim != common.Hash(rootClaim) {
		return nil, nil, nil, errors.Errorf(
			"rootClaim is not keccak256(extraData): rootClaim=%s keccak256(extraData)=%s",
			common.Bytes2Hex(rootClaim[:]), superRoot.RootClaim.String())
	}
	fmt.Printf("expected gameId=%s timestamp=%d, rootClaim=%s\n", common.Bytes2Hex(gameId[:]), superRoot.Timestamp, common.Bytes2Hex(rootClaim[:]))
	for _, entry := range superRoot.Entries {
		fmt.Printf("  super root entry: chainId=%s outputRoot=%s\n", entry.ChainID, entry.OutputRoot.String())
	}

	// Get GameUUID. extraData is a dynamic `bytes`, so it must be passed in full: the
	// UUID commits to the whole preimage.
	gameUUID, err := config.DisputeGameFactoryCaller.GetGameUUID(nil, targetGameType, rootClaim, gameResult.ExtraData)
	if err != nil {
		return nil, nil, nil, errors.WithStack(err)
	}
	slotForGameId := CalculateMappingSlotBytes(gameUUID[:], uint64(103))
	fmt.Printf("gameUUID=%s, slotForGameId %v\n", common.Bytes2Hex(gameUUID[:]), slotForGameId.String())

	l1ProofGetter, err := client.NewETHClientWith(config.L1Client)
	if err != nil {
		return nil, nil, nil, errors.WithStack(err)
	}
	// Get Proof of DisputeGameFactoryProxy.sol
	marshallSlotForGameId, err := slotForGameId.MarshalText()
	if err != nil {
		return nil, nil, nil, errors.WithStack(err)
	}
	disputeGameFactoryAccountProof, err := l1ProofGetter.GetProof(ctx,
		config.DisputeGameFactoryAddress,
		[][]byte{marshallSlotForGameId},
		big.NewInt(0).SetUint64(l1Header.BlockNumber))
	if err != nil {
		return nil, nil, nil, errors.WithStack(err)
	}

	// Get Proof of SuperFaultDisputeGame.sol. The FaultDisputeGame binding is reused
	// because only l1Head() and status() are called on it, and both have the same
	// selector on the super game; l2BlockNumber() does not exist there.
	gameType, timestamp, gameAddress := UnpackGameId(gameId)
	faultDisputeGameCaller, err := bindings2.NewFaultDisputeGameCaller(gameAddress, config.L1Client)
	if err != nil {
		return nil, nil, nil, errors.WithStack(err)
	}
	status, err := faultDisputeGameCaller.Status(nil)
	if err != nil {
		return nil, nil, nil, errors.WithStack(err)
	}
	fmt.Printf("gameType=%d, timestamp=%d, gameAddress=%s, status=%d\n", gameType, timestamp, gameAddress, status)
	marshalSlotForStatus, err := common.BigToHash(big.NewInt(0)).MarshalText()
	if err != nil {
		return nil, nil, nil, errors.WithStack(err)
	}
	faultDisputeGameProof, err := l1ProofGetter.GetProof(ctx, gameAddress,
		[][]byte{marshalSlotForStatus},
		big.NewInt(0).SetUint64(l1Header.BlockNumber),
	)
	if err != nil {
		return nil, nil, nil, errors.WithStack(err)
	}

	disputeGameFactoryProof := types.FaultDisputeGameProof{
		StateRoot: l1Header.StateRoot,
		DisputeGameFactoryAccount: &lctypes.AccountUpdate{
			AccountProof:       disputeGameFactoryAccountProof.AccountProofRLP,
			AccountStorageRoot: disputeGameFactoryAccountProof.StorageHash[:],
		},
		DisputeGameFactoryGameIdProof: disputeGameFactoryAccountProof.StorageProofRLP[0],
		FaultDisputeGameAccount: &lctypes.AccountUpdate{
			AccountProof:       faultDisputeGameProof.AccountProofRLP,
			AccountStorageRoot: faultDisputeGameProof.StorageHash[:],
		},
		FaultDisputeGameGameStatusProof: faultDisputeGameProof.StorageProofRLP[0],
		FaultDisputeGameSourceGameType:  gameType,
	}

	return superRoot, &disputeGameFactoryProof, faultDisputeGameCaller, nil
}
