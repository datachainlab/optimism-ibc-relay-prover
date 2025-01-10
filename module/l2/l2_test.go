package l2

import (
	"context"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/datachainlab/ethereum-ibc-relay-chain/pkg/relay/ethereum"
	"github.com/datachainlab/ibc-hd-signer/pkg/hd"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/config"
	"github.com/ethereum/go-ethereum/common"
	"github.com/hyperledger-labs/yui-relayer/core"
	"github.com/hyperledger-labs/yui-relayer/log"
	"github.com/stretchr/testify/suite"
	"os"
	"testing"
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

	config := config.ProverConfig{
		OpNodeEndpoint:        "http://localhost:7545",
		L1ExecutionEndpoint:   "http://localhost:8545",
		PreimageMakerEndpoint: "http://localhost:10080",
	}
	ts.l2Client = NewL2Client(&config, l2Chain)
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
