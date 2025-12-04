package util

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/cockroachdb/errors"
)

type HTTPClient struct {
	timeout time.Duration
}

func NewHTTPClient(timeout time.Duration) *HTTPClient {
	return &HTTPClient{timeout: timeout}
}

func (c *HTTPClient) POST(ctx context.Context, url string, request interface{}) ([]byte, error) {
	httpClient := http.Client{
		Timeout: c.timeout,
	}
	var buffer *bytes.Buffer
	if request != nil {
		body, err := json.Marshal(request)
		if err != nil {
			return nil, errors.Wrap(err, "failed to marshal request")
		}
		buffer = bytes.NewBuffer(body)
	}

	httpRequest, err := http.NewRequestWithContext(ctx, "POST", url, buffer)
	if err != nil {
		return nil, errors.Wrap(err, "failed to call request")
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := httpClient.Do(httpRequest)
	if err != nil {
		return nil, errors.Wrap(err, "failed to call http request")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.Errorf("failed to create preimages: status=%d", response.StatusCode)
	}
	return io.ReadAll(response.Body)
}
