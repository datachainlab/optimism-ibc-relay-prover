package l1

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/datachainlab/ethereum-ibc-relay-prover/beacon"
	lctypes "github.com/datachainlab/ethereum-ibc-relay-prover/light-clients/ethereum/types"
	ethprover "github.com/datachainlab/ethereum-ibc-relay-prover/relay"
	"github.com/hyperledger-labs/yui-relayer/log"
	"io/ioutil"
	"net/http"
)

func (pr *L1Client) secondsPerSlot() uint64 {
	return ethprover.SecondsPerSlot(ethprover.IsMainnetPreset(pr.network))
}

func (pr *L1Client) slotsPerEpoch() uint64 {
	return ethprover.SlotsPerEpoch(ethprover.IsMainnetPreset(pr.network))
}

func (pr *L1Client) epochsPerSyncCommitteePeriod() uint64 {
	return ethprover.EpochsPerSyncCommitteePeriod(ethprover.IsMainnetPreset(pr.network))
}

// returns the first slot of the period
func (pr *L1Client) getPeriodBoundarySlot(period uint64) uint64 {
	return period * pr.epochsPerSyncCommitteePeriod() * pr.slotsPerEpoch()
}

func (pr *L1Client) computeSyncCommitteePeriod(epoch uint64) uint64 {
	return epoch / pr.epochsPerSyncCommitteePeriod()
}

func (pr *L1Client) computeEpoch(slot uint64) uint64 {
	return slot / pr.slotsPerEpoch()
}

func (pr *L1Client) GetSlotAtTimestamp(timestamp uint64) (uint64, error) {
	genesis, err := pr.beaconClient.GetGenesis()
	if err != nil {
		return 0, err
	}
	if timestamp < genesis.GenesisTimeSeconds {
		return 0, fmt.Errorf("computeSlotAtTimestamp: timestamp is smaller than genesisTime: timestamp=%v genesisTime=%v", timestamp, genesis.GenesisTimeSeconds)
	} else if (timestamp-genesis.GenesisTimeSeconds)%pr.secondsPerSlot() != 0 {
		return 0, fmt.Errorf("computeSlotAtTimestamp: timestamp is not multiple of secondsPerSlot: timestamp=%v secondsPerSlot=%v genesisTime=%v", timestamp, pr.secondsPerSlot(), genesis.GenesisTimeSeconds)
	}
	slotsSinceGenesis := (timestamp - genesis.GenesisTimeSeconds) / pr.secondsPerSlot()
	return ethprover.GENESIS_SLOT + slotsSinceGenesis, nil
}

// returns a period corresponding to a given execution block number
func (pr *L1Client) getPeriodWithBlockNumber(blockNumber uint64) (uint64, error) {
	timestamp, err := pr.TimestampAt(context.Background(), blockNumber)
	if err != nil {
		return 0, err
	}
	slot, err := pr.GetSlotAtTimestamp(timestamp)
	if err != nil {
		return 0, err
	}
	return pr.computeSyncCommitteePeriod(pr.computeEpoch(slot)), nil
}

func (pr *L1Client) buildExecutionUpdate(executionHeader *beacon.ExecutionPayloadHeader) (*lctypes.ExecutionUpdate, error) {
	return ethprover.BuildExecutionUpdate(executionHeader)
}

// To avoid SupportedVersion check due to lighthouse doesn't include version(fork name)
type BeaconClient struct {
	endpoint string
}

func NewBeaconClient(endpoint string) BeaconClient {
	return BeaconClient{endpoint: endpoint}
}

func (cl BeaconClient) GetGenesis() (*beacon.Genesis, error) {
	var res beacon.GenesisResponse
	if err := cl.get("/eth/v1/beacon/genesis", &res); err != nil {
		return nil, err
	}
	return beacon.ToGenesis(res)
}

func (cl BeaconClient) GetBlockRoot(slot uint64, allowOptimistic bool) (*beacon.BlockRootResponse, error) {
	var res beacon.BlockRootResponse
	if err := cl.get(fmt.Sprintf("/eth/v1/beacon/blocks/%v/root", slot), &res); err != nil {
		return nil, err
	}
	if !allowOptimistic && res.ExecutionOptimistic {
		return nil, fmt.Errorf("optimistic execution not allowed")
	}
	return &res, nil
}

func (cl BeaconClient) GetFinalityCheckpoints() (*beacon.StateFinalityCheckpoints, error) {
	var res beacon.StateFinalityCheckpointResponse
	if err := cl.get("/eth/v1/beacon/states/head/finality_checkpoints", &res); err != nil {
		return nil, err
	}
	return beacon.ToStateFinalityCheckpoints(res)
}

func (cl BeaconClient) GetBootstrap(finalizedRoot []byte) (*beacon.LightClientBootstrapResponse, error) {
	if len(finalizedRoot) != 32 {
		return nil, fmt.Errorf("finalizedRoot length must be 32: actual=%v", finalizedRoot)
	}
	var res beacon.LightClientBootstrapResponse
	if err := cl.get(fmt.Sprintf("/eth/v1/beacon/light_client/bootstrap/0x%v", hex.EncodeToString(finalizedRoot[:])), &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (cl BeaconClient) GetLightClientUpdates(period uint64, count uint64) (beacon.LightClientUpdatesResponse, error) {
	var res beacon.LightClientUpdatesResponse
	if err := cl.get(fmt.Sprintf("/eth/v1/beacon/light_client/updates?start_period=%v&count=%v", period, count), &res); err != nil {
		return nil, err
	}
	//FIXME count is ignored in lighthouse.
	/*if len(res) != int(count) {
		return nil, fmt.Errorf("unexpected response length: period=%d, expected=%v actual=%v", period, count, len(res))
	}*/
	return res[0:count], nil
}

func (cl BeaconClient) GetLightClientUpdate(period uint64) (*beacon.LightClientUpdateResponse, error) {
	res, err := cl.GetLightClientUpdates(period, 1)
	if err != nil {
		return nil, err
	}
	return &res[0], nil
}

func (cl BeaconClient) GetLightClientFinalityUpdate() (*beacon.LightClientFinalityUpdateResponse, error) {
	var res beacon.LightClientFinalityUpdateResponse
	if err := cl.get("/eth/v1/beacon/light_client/finality_update", &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (cl BeaconClient) get(path string, res any) error {
	log.GetLogger().Debug("Beacon API request", "endpoint", cl.endpoint+path)
	r, err := http.Get(cl.endpoint + path)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	bz, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return err
	}
	if r.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d %s", r.StatusCode, string(bz))
	}
	return json.Unmarshal(bz, &res)
}
