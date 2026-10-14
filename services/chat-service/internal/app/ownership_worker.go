package app

import (
	"context"
	"sync"
	"time"
)

func (b *bootstrap) startOwnershipWorker() {
	if b.stores.ownership == nil || b.realtime.hub == nil {
		return
	}
	metrics := newOwnershipOutboxMetrics(b.metrics)
	ctx, cancel := workerLifecycle()
	b.workers.ownershipCancel = cancel
	b.workers.ownershipWG = &sync.WaitGroup{}
	b.workers.ownershipWG.Add(1)
	go func() {
		defer b.workers.ownershipWG.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				b.dispatchOwnershipChanges(ctx, metrics)
			}
		}
	}()
}

func (b *bootstrap) dispatchOwnershipChanges(ctx context.Context, metrics *ownershipOutboxMetrics) {
	b.observeOwnershipBacklog(ctx, metrics)
	if err := b.stores.ownership.DispatchOwnershipChanges(ctx, b.broadcaster().PublishOwnershipUpdated, metrics.attempt); err != nil && ctx.Err() == nil {
		b.logger.WarnContext(ctx, "ownership invalidation dispatch failed")
	}
	b.observeOwnershipBacklog(ctx, metrics)
}

func (b *bootstrap) observeOwnershipBacklog(ctx context.Context, metrics *ownershipOutboxMetrics) {
	if metrics == nil {
		return
	}
	pending, oldestAge, err := b.stores.ownership.OwnershipOutboxBacklog(ctx)
	if err != nil {
		if ctx.Err() == nil {
			b.logger.WarnContext(ctx, "ownership backlog read failed")
		}
		return
	}
	metrics.backlog(pending, oldestAge)
}
