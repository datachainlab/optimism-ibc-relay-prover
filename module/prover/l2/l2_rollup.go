package l2

import (
	"bytes"
	"encoding/json"
	"github.com/cockroachdb/errors"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"io"
	"net/http"
)

func (c *L2Client) SyncStatus() (*SyncStatus, error) {
	data, err := c.call("optimism_syncStatus", nil)
	if err != nil {
		return nil, err
	}
	var syncStatus SyncStatus
	if err := json.Unmarshal(data, &syncStatus); err != nil {
		return nil, errors.WithStack(err)
	}
	return &syncStatus, nil
}

func (c *L2Client) RollupConfigBytes() ([]byte, error) {
	data, err := c.call("optimism_rollupConfig", nil)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (c *L2Client) OutputAtBlock(blockNumber uint64) (*OutputResponse, error) {
	data, err := c.call("optimism_outputAtBlock", []interface{}{hexutil.Uint64(blockNumber)})
	if err != nil {
		return nil, err
	}
	var outputResponse OutputResponse
	if err = json.Unmarshal(data, &outputResponse); err != nil {
		return nil, errors.WithStack(err)
	}
	return &outputResponse, nil
}

func (c *L2Client) call(method string, params []interface{}) ([]byte, error) {
	type RpcRequest struct {
		JsonRPC string        `json:"jsonrpc"`
		Method  string        `json:"method"`
		Params  []interface{} `json:"params"`
		Id      int           `json:"id"`
	}
	request := &RpcRequest{
		JsonRPC: "2.0",
		Method:  method,
		Params:  params,
		Id:      1,
	}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	httpClient := http.Client{
		Timeout: c.preimageMakerTimeout,
	}
	response, err := httpClient.Post(c.opNodeEndpoint, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return nil, errors.WithStack(err)
	}
	if response.StatusCode != http.StatusOK {
		return nil, errors.Errorf("failed to get sync status: %d", response.StatusCode)
	}
	buf, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	type RpcResult struct {
		Result json.RawMessage `json:"result"`
	}
	var result RpcResult
	if err = json.Unmarshal(buf, &result); err != nil {
		return nil, errors.WithStack(err)
	}
	if result.Result == nil {
		return nil, errors.Errorf("response has no result : %s", string(buf))
	}
	return result.Result, err
}
