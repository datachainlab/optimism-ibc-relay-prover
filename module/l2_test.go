package module

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/datachainlab/ethereum-ibc-relay-chain/pkg/relay/ethereum"
	"github.com/datachainlab/ibc-hd-signer/pkg/hd"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/hyperledger-labs/yui-relayer/core"
	"github.com/hyperledger-labs/yui-relayer/log"
	"github.com/stretchr/testify/suite"
	"math/big"
	"net/http"
	"os"
	"testing"
	"time"
)

type L2TestSuite struct {
	suite.Suite
	l2Client *L2Client
}

func TestL2TestSuite(t *testing.T) {
	suite.Run(t, new(L2TestSuite))
}

func (ts *L2TestSuite) SetupTest() {
	err := log.InitLogger("DEBUG", "text", "stdout")
	ts.Require().NoError(err)

	addressHex, err := os.ReadFile("../tests/contracts/addresses/OwnableIBCHandler")
	ts.Require().NoError(err)

	signerConfig := &hd.SignerConfig{
		Mnemonic: "test test test test test test test test test test test junk",
		Path:     "m/44'/60'/0'/0/0",
	}
	anySignerConfig, err := codectypes.NewAnyWithValue(signerConfig)
	ts.Require().NoError(err)
	l2Chain, err := ethereum.NewChain(ethereum.ChainConfig{
		RpcAddr:    "http://localhost:9545",
		IbcAddress: common.Bytes2Hex(addressHex),
		Signer:     anySignerConfig,
	})
	ts.Require().NoError(err)
	codec := core.MakeCodec()

	err = l2Chain.Init("", 0, codec, false)
	ts.Require().NoError(err)

	err = l2Chain.SetRelayInfo(&core.PathEnd{
		ClientID:     "mock-client-0",
		ConnectionID: "connection-0",
		ChannelID:    "channel-0",
		PortID:       "transfer",
		Order:        "UNORDERED",
	}, nil, nil)
	ts.Require().NoError(err)

	config := ProverConfig{
		OpNodeEndpoint:        "http://localhost:7545",
		L1ExecutionEndpoint:   "http://localhost:8545",
		PreimageMakerEndpoint: "http://localhost:10080",
		L1L2DistanceThreshold: 10,
	}
	ts.l2Client = NewL2Client(&config, l2Chain)
}

func (ts *L2TestSuite) TestOutputAtBlock() {
	targetNumber := uint64(42227)
	output, err := ts.l2Client.OutputAtBlock(targetNumber)
	ts.Require().NoError(err)
	agreedNumber := targetNumber - 1

	targetOutput, err := ts.l2Client.OutputAtBlock(targetNumber)
	ts.Require().NoError(err)
	agreedOutput, err := ts.l2Client.OutputAtBlock(agreedNumber)
	ts.Require().NoError(err)

	type RawL2Derivation struct {
		L1HeadHash         common.Hash `json:"l1_head_hash"`
		AgreedL2HeadHash   common.Hash `protobuf:"bytes,2,opt,name=agreed_l2_head_hash,json=agreedL2HeadHash,proto3" json:"agreed_l2_head_hash,omitempty"`
		AgreedL2OutputRoot common.Hash `protobuf:"bytes,3,opt,name=agreed_l2_output_root,json=agreedL2OutputRoot,proto3" json:"agreed_l2_output_root,omitempty"`
		L2HeadHash         common.Hash `protobuf:"bytes,4,opt,name=l2_head_hash,json=l2HeadHash,proto3" json:"l2_head_hash,omitempty"`
		L2OutputRoot       common.Hash `protobuf:"bytes,5,opt,name=l2_output_root,json=l2OutputRoot,proto3" json:"l2_output_root,omitempty"`
		L2BlockNumber      uint64      `protobuf:"varint,6,opt,name=l2_block_number,json=l2BlockNumber,proto3" json:"l2_block_number,omitempty"`
	}

	l1Client, err := ethclient.Dial("http://localhost:8545")
	ts.Require().NoError(err)

	httpClient := http.Client{}
	//	l1 := big.NewInt(0).SetUint64(output.Status.FinalizedL1.Number)
	l1 := big.NewInt(0).SetUint64(targetOutput.BlockRef.L1Origin.Number + 10)
	header, err := l1Client.HeaderByNumber(context.Background(), l1)
	ts.Require().NoError(err)

	derivations := []*RawL2Derivation{{
		L1HeadHash:         header.Hash(),
		AgreedL2HeadHash:   agreedOutput.BlockRef.Hash,
		AgreedL2OutputRoot: common.BytesToHash(agreedOutput.OutputRoot[:]),
		L2HeadHash:         targetOutput.BlockRef.Hash,
		L2OutputRoot:       common.BytesToHash(targetOutput.OutputRoot[:]),
		L2BlockNumber:      targetNumber,
	}}
	str, err := json.MarshalIndent(derivations, "", "  ")
	ts.Require().NoError(err)
	println(header.Number.Uint64(), output.BlockRef.L1Origin.Number)
	println(string(str))

	body, err := json.Marshal(derivations)
	ts.Require().NoError(err)

	buffer := bytes.NewBuffer(body)
	start := time.Now()
	_, err = httpClient.Post(fmt.Sprintf("%s/derivation", "http://localhost:10080"), "application/json", buffer)
	ts.Require().NoError(err)
	end := time.Now()

	//preimageData, err := io.ReadAll(response.Body)

	ts.Require().NoError(err)
	println(end.Sub(start).Milliseconds())
}

func (ts *L2TestSuite) TestSetupDerivation() {
	ctx := context.Background()
	latest, err := ts.l2Client.LatestDerivation(ctx)
	ts.Require().NoError(err)

	lastAgreedNumber := latest.L2.L2BlockNumber - 1
	derivations, err := ts.l2Client.SetupDerivations(context.Background(), lastAgreedNumber-10, lastAgreedNumber, latest.L1.Number)
	ts.Require().NoError(err)
	for _, d := range derivations {
		println(d.L2.L2BlockNumber, d.L1.Number)
	}
	// Create preimage data for all derivations
	preimages, err := ts.l2Client.CreatePreimages(context.Background(), derivations)
	ts.Require().NoError(err)
	ts.Require().True(len(preimages) > 0)
}
