package app

import (
	"sync"
	"time"
)

func (b *bootstrap) startOwnershipWorker() {
	if b.stores.ownership == nil || b.realtime.hub == nil {
		return
	}
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
				if err := b.stores.ownership.DispatchOwnershipChanges(ctx, b.broadcaster().PublishOwnershipUpdated); err != nil && ctx.Err() == nil {
					b.logger.WarnContext(ctx, "ownership invalidation dispatch failed")
				}
			}
		}
	}()
}
