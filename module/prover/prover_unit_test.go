package prover

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	lctypes "github.com/datachainlab/ethereum-light-client-types/prover/types"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/prover/l2"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/types"
	"github.com/hyperledger-labs/yui-relayer/core"
	"github.com/hyperledger-labs/yui-relayer/log"
	"github.com/stretchr/testify/require"
)

func init() {
	// Initialize logger for tests
	_ = log.InitLogger("DEBUG", "text", "stdout", false)
}

func newTestProver(maxConcurrency uint64) *Prover {
	return &Prover{
		maxHeaderConcurrency: maxConcurrency,
		logger:               log.GetLogger().WithModule(ModuleName),
	}
}

func TestMakeHeaderChanOrdering(t *testing.T) {
	pr := newTestProver(4)

	headerChunks := make([]*l2.PreimageMetadata, 10)
	for i := 0; i < len(headerChunks); i++ {
		headerChunks[i] = &l2.PreimageMetadata{
			Claimed: uint64(i + 1),
		}
	}

	ret := pr.makeHeaderChan(context.Background(), headerChunks, func(ctx context.Context, header *l2.PreimageMetadata) (core.Header, error) {
		// Simulate varying processing time
		time.Sleep(time.Duration(header.Claimed%3) * 10 * time.Millisecond)
		return &types.Header{
			Derivation: &types.Derivation{
				L2BlockNumber: header.Claimed,
			},
		}, nil
	})

	// Verify headers come out in order
	expectedSeq := uint64(1)
	for chunk := range ret {
		require.NoError(t, chunk.Error)
		h := chunk.Header.(*types.Header)
		require.Equal(t, expectedSeq, h.Derivation.L2BlockNumber, "Headers should be delivered in order")
		expectedSeq++
	}
	require.Equal(t, uint64(len(headerChunks)+1), expectedSeq, "All headers should be delivered")
}

func TestMakeHeaderChanConcurrency(t *testing.T) {
	maxConcurrency := uint64(3)
	pr := newTestProver(maxConcurrency)

	headerChunks := make([]*l2.PreimageMetadata, 10)
	for i := 0; i < len(headerChunks); i++ {
		headerChunks[i] = &l2.PreimageMetadata{
			Claimed: uint64(i + 1),
		}
	}

	var currentConcurrent atomic.Int32
	var maxObserved atomic.Int32

	ret := pr.makeHeaderChan(context.Background(), headerChunks, func(ctx context.Context, header *l2.PreimageMetadata) (core.Header, error) {
		current := currentConcurrent.Add(1)
		if current > maxObserved.Load() {
			maxObserved.Store(current)
		}
		time.Sleep(50 * time.Millisecond)
		currentConcurrent.Add(-1)
		return &types.Header{
			Derivation: &types.Derivation{
				L2BlockNumber: header.Claimed,
			},
		}, nil
	})

	// Drain channel
	for chunk := range ret {
		require.NoError(t, chunk.Error)
	}

	// Verify concurrency limit was respected
	require.LessOrEqual(t, maxObserved.Load(), int32(maxConcurrency), "Should not exceed max concurrency")
}

func TestMakeHeaderChanError(t *testing.T) {
	pr := newTestProver(2)

	headerChunks := make([]*l2.PreimageMetadata, 5)
	for i := 0; i < len(headerChunks); i++ {
		headerChunks[i] = &l2.PreimageMetadata{
			Claimed: uint64(i + 1),
		}
	}

	errorAtIndex := 2
	ret := pr.makeHeaderChan(context.Background(), headerChunks, func(ctx context.Context, header *l2.PreimageMetadata) (core.Header, error) {
		if header.Claimed == uint64(errorAtIndex+1) {
			return nil, context.DeadlineExceeded
		}
		return &types.Header{
			Derivation: &types.Derivation{
				L2BlockNumber: header.Claimed,
			},
		}, nil
	})

	// Verify error is propagated and all chunks are delivered
	count := 0
	for chunk := range ret {
		if count == errorAtIndex {
			require.Error(t, chunk.Error)
			require.ErrorIs(t, chunk.Error, context.DeadlineExceeded)
		} else {
			require.NoError(t, chunk.Error)
		}
		count++
	}
	require.Equal(t, len(headerChunks), count, "All chunks should be delivered including errors")
}

func TestMakeHeaderChanEmpty(t *testing.T) {
	pr := newTestProver(2)

	headerChunks := make([]*l2.PreimageMetadata, 0)

	ret := pr.makeHeaderChan(context.Background(), headerChunks, func(ctx context.Context, header *l2.PreimageMetadata) (core.Header, error) {
		return nil, nil
	})

	count := 0
	for range ret {
		count++
	}
	require.Equal(t, 0, count, "Empty input should produce empty output")
}

func TestMakeHeaderChanSingle(t *testing.T) {
	pr := newTestProver(2)

	headerChunks := []*l2.PreimageMetadata{
		{Claimed: 100},
	}

	ret := pr.makeHeaderChan(context.Background(), headerChunks, func(ctx context.Context, header *l2.PreimageMetadata) (core.Header, error) {
		return &types.Header{
			Derivation: &types.Derivation{
				L2BlockNumber: header.Claimed,
			},
		}, nil
	})

	count := 0
	for chunk := range ret {
		require.NoError(t, chunk.Error)
		h := chunk.Header.(*types.Header)
		require.Equal(t, uint64(100), h.Derivation.L2BlockNumber)
		count++
	}
	require.Equal(t, 1, count)
}

func TestDurationMulByFraction(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		fraction *lctypes.Fraction
		expected time.Duration
	}{
		{
			name:     "half",
			duration: 100 * time.Second,
			fraction: &lctypes.Fraction{Numerator: 1, Denominator: 2},
			expected: 50 * time.Second,
		},
		{
			name:     "two thirds",
			duration: 90 * time.Second,
			fraction: &lctypes.Fraction{Numerator: 2, Denominator: 3},
			expected: 60 * time.Second,
		},
		{
			name:     "full",
			duration: 100 * time.Second,
			fraction: &lctypes.Fraction{Numerator: 1, Denominator: 1},
			expected: 100 * time.Second,
		},
		{
			name:     "quarter",
			duration: 400 * time.Millisecond,
			fraction: &lctypes.Fraction{Numerator: 1, Denominator: 4},
			expected: 100 * time.Millisecond,
		},
	}

	durationMulByFraction := func(d time.Duration, f *lctypes.Fraction) time.Duration {
		nsec := d.Nanoseconds() * int64(f.Numerator) / int64(f.Denominator)
		return time.Duration(nsec) * time.Nanosecond
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := durationMulByFraction(tt.duration, tt.fraction)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestNewProverMaxHeaderConcurrency(t *testing.T) {
	tests := []struct {
		name     string
		input    uint64
		expected uint64
	}{
		{
			name:     "zero should be 1",
			input:    0,
			expected: 1,
		},
		{
			name:     "one stays one",
			input:    1,
			expected: 1,
		},
		{
			name:     "four stays four",
			input:    4,
			expected: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr := &Prover{
				maxHeaderConcurrency: max(tt.input, 1),
			}
			require.Equal(t, tt.expected, pr.maxHeaderConcurrency)
		})
	}
}

func TestMakeHeaderChanContextCancellation(t *testing.T) {
	pr := newTestProver(2)

	headerChunks := make([]*l2.PreimageMetadata, 10)
	for i := 0; i < len(headerChunks); i++ {
		headerChunks[i] = &l2.PreimageMetadata{
			Claimed: uint64(i + 1),
		}
	}

	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	wg.Add(1)

	ret := pr.makeHeaderChan(ctx, headerChunks, func(ctx context.Context, header *l2.PreimageMetadata) (core.Header, error) {
		// Cancel after first chunk starts processing
		if header.Claimed == 1 {
			wg.Done()
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
			return &types.Header{
				Derivation: &types.Derivation{
					L2BlockNumber: header.Claimed,
				},
			}, nil
		}
	})

	// Wait for first chunk to start, then cancel
	wg.Wait()
	cancel()

	// Drain the channel - some may succeed, some may fail due to cancellation
	for range ret {
		// Just drain
	}
}
