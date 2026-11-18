package sandbox

import (
	"context"

	"golang.org/x/sync/semaphore"
)

// inflightCostFactor accounts for the parent-side copies that coexist while an
// Eval frame is encoded: the normalized vars tree, the JSON buffer itself and
// the transient growth of encoding/json's buffer. It is a conservative
// accounting multiplier, not a measurement of Go heap bytes.
const inflightCostFactor = 2

// byteBudget bounds the total admitted input bytes across concurrent remote
// evaluations. It does not include worker memory, HTTP replies or result
// frames; those have independent frame limits.
type byteBudget struct {
	sem      *semaphore.Weighted
	capacity int64
}

func newByteBudget(capacity int64) *byteBudget {
	if capacity <= 0 {
		capacity = 1
	}
	return &byteBudget{sem: semaphore.NewWeighted(capacity), capacity: capacity}
}

// acquire waits for room to encode inputBytes. It returns the context error
// when admission cannot be granted before the queue deadline.
func (b *byteBudget) acquire(ctx context.Context, inputBytes int) error {
	if b == nil || inputBytes <= 0 {
		return nil
	}
	cost := int64(inputBytes) * inflightCostFactor
	if cost > b.capacity {
		return ErrSandboxOutputLimit
	}
	return b.sem.Acquire(ctx, cost)
}

func (b *byteBudget) release(inputBytes int) {
	if b == nil || inputBytes <= 0 {
		return
	}
	b.sem.Release(int64(inputBytes) * inflightCostFactor)
}
