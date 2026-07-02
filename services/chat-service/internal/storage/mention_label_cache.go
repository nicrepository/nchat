package storage

import (
	"context"
	"fmt"
	"time"

	valkey "github.com/valkey-io/valkey-go"
)

const mentionLabelCachePrefix = "nchat:chat:mention-label:"

// ValkeyMentionLabelCache stores workspace-scoped mention labels with the TTL
// supplied by the message service.
type ValkeyMentionLabelCache struct {
	client valkey.Client
}

func NewValkeyMentionLabelCache(valkeyURL string) (*ValkeyMentionLabelCache, error) {
	option, err := valkey.ParseURL(valkeyURL)
	if err != nil {
		return nil, fmt.Errorf("parse mention label cache URL: %w", err)
	}
	client, err := valkey.NewClient(option)
	if err != nil {
		return nil, fmt.Errorf("create mention label cache client: %w", err)
	}
	return newValkeyMentionLabelCache(client), nil
}

func newValkeyMentionLabelCache(client valkey.Client) *ValkeyMentionLabelCache {
	return &ValkeyMentionLabelCache{client: client}
}

func (c *ValkeyMentionLabelCache) Get(ctx context.Context, workspaceID string, refs []string) (map[string]string, error) {
	labels := make(map[string]string, len(refs))
	if len(refs) == 0 {
		return labels, nil
	}
	keys := make([]string, len(refs))
	refByKey := make(map[string]string, len(refs))
	for i, ref := range refs {
		keys[i] = mentionLabelCacheKey(workspaceID, ref)
		refByKey[keys[i]] = ref
	}
	values, err := valkey.MGet(c.client, ctx, keys)
	if err != nil {
		return nil, fmt.Errorf("get mention labels from cache: %w", err)
	}
	for key, value := range values {
		label, err := value.ToString()
		if valkey.IsValkeyNil(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("decode mention label from cache: %w", err)
		}
		labels[refByKey[key]] = label
	}
	return labels, nil
}

func (c *ValkeyMentionLabelCache) Set(ctx context.Context, workspaceID string, labels map[string]string, ttl time.Duration) error {
	if len(labels) == 0 {
		return nil
	}
	commands := make([]valkey.Completed, 0, len(labels))
	for ref, label := range labels {
		commands = append(commands, c.client.B().Set().Key(mentionLabelCacheKey(workspaceID, ref)).Value(label).Ex(ttl).Build())
	}
	for _, result := range c.client.DoMulti(ctx, commands...) {
		if err := result.Error(); err != nil {
			return fmt.Errorf("set mention label in cache: %w", err)
		}
	}
	return nil
}

func (c *ValkeyMentionLabelCache) Close() {
	c.client.Close()
}

func mentionLabelCacheKey(workspaceID, ref string) string {
	return mentionLabelCachePrefix + "{" + workspaceID + "}:" + ref
}
