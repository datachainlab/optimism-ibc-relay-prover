package l2

import (
	"context"
	"encoding/json"
	"github.com/cockroachdb/errors"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/rpc"
	"net/http"
)

func (c *L2Client) SyncStatus(ctx context.Context) (*SyncStatus, error) {
	var resp SyncStatus
	err := c.call(ctx, "optimism_syncStatus", &resp)
	return &resp, err
}

func (c *L2Client) RollupConfigBytes(ctx context.Context) ([]byte, error) {
	var resp json.RawMessage
	err := c.call(ctx, "optimism_rollupConfig", &resp)
	return resp, err
}

func (c *L2Client) OutputAtBlock(ctx context.Context, blockNumber uint64) (*OutputResponse, error) {
	var resp OutputResponse
	err := c.call(ctx, "optimism_outputAtBlock", &resp, hexutil.Uint64(blockNumber))
	return &resp, err
}

func (c *L2Client) call(ctx context.Context, method string, result interface{}, args ...interface{}) error {
	httpClient := http.Client{
		Timeout: c.opNodeTimeout,
	}
	jsonClient, err := rpc.DialOptions(ctx, c.opNodeEndpoint, rpc.WithHTTPClient(&httpClient))
	if err = jsonClient.CallContext(ctx, result, method, args...); err != nil {
		return errors.Wrapf(err, "failed to make http request for op-node: method=%s", method)
	}
	return nil
}
