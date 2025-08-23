package l2

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/cockroachdb/errors"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"io"
	"net/http"
)

func (c *L2Client) SyncStatus(ctx context.Context) (*SyncStatus, error) {
	data, err := c.call(ctx, "optimism_syncStatus", nil)
	if err != nil {
		return nil, errors.Wrap(err, "failed to call syncStatus")
	}
	var syncStatus SyncStatus
	if err = json.Unmarshal(data, &syncStatus); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal syncStatus")
	}
	c.logger.InfoContext(ctx, "SyncStatus", "finalizedL2", syncStatus.FinalizedL2.Number)
	return &syncStatus, nil
}

func (c *L2Client) RollupConfigBytes(ctx context.Context) ([]byte, error) {
	data, err := c.call(ctx, "optimism_rollupConfig", nil)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get rollup config")
	}
	return data, nil
}

func (c *L2Client) OutputAtBlock(ctx context.Context, blockNumber uint64) (*OutputResponse, error) {
	data, err := c.call(ctx, "optimism_outputAtBlock", []interface{}{hexutil.Uint64(blockNumber)})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get output at block: number=%d", blockNumber)
	}
	var outputResponse OutputResponse
	if err = json.Unmarshal(data, &outputResponse); err != nil {
		return nil, errors.Wrapf(err, "failed to unmarshal output root: number=%d", blockNumber)
	}
	return &outputResponse, nil
}

func (c *L2Client) call(ctx context.Context, method string, params []interface{}) ([]byte, error) {
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
		return nil, errors.Wrapf(err, "failed to marshal op-node request: method=%s", method)
	}

	httpClient := http.Client{
		Timeout: c.opNodeTimeout,
	}
	httpRequest, err := http.NewRequestWithContext(ctx, "POST", c.opNodeEndpoint, bytes.NewBuffer(body))
	if err != nil {
		return nil, errors.Wrapf(err, "failed to make http request for op-node: method=%s", method)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := httpClient.Do(httpRequest)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to call op-node: method=%s", method)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.Errorf("failed to get sync status: %d", response.StatusCode)
	}
	buf, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read response from op-node: method=%s", method)
	}

	type RpcResult struct {
		Result json.RawMessage `json:"result"`
	}
	var result RpcResult
	if err = json.Unmarshal(buf, &result); err != nil {
		return nil, errors.Wrapf(err, "failed to unmarshal from op-node: method=%s", method)
	}
	if result.Result == nil {
		return nil, errors.Errorf("response has no result from op-node: value=%s, method=%s", string(buf), method)
	}
	return result.Result, err
}
