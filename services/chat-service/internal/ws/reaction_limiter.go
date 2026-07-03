package ws

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	valkey "github.com/valkey-io/valkey-go"
)

const reactionActionsPerMinute = 60

var reactionRateScript = valkey.NewLuaScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then redis.call('EXPIRE', KEYS[1], ARGV[1]) end
return count`)

type ValkeyReactionLimiter struct{ client valkey.Client }

func NewValkeyReactionLimiter(valkeyURL string) (*ValkeyReactionLimiter, error) {
	if valkeyURL == "" {
		return nil, errors.New("reaction limiter requires VALKEY_URL")
	}
	option, err := valkey.ParseURL(valkeyURL)
	if err != nil {
		return nil, fmt.Errorf("parse reaction limiter valkey URL: %w", err)
	}
	client, err := valkey.NewClient(option)
	if err != nil {
		return nil, fmt.Errorf("create reaction limiter client: %w", err)
	}
	return &ValkeyReactionLimiter{client: client}, nil
}

func (l *ValkeyReactionLimiter) Allow(ctx context.Context, userID string) (bool, error) {
	sum := sha256.Sum256([]byte(userID))
	key := "nchat:chat:action:reaction:" + hex.EncodeToString(sum[:])
	count, err := reactionRateScript.Exec(ctx, l.client, []string{key}, []string{strconv.Itoa(60)}).AsInt64()
	if err != nil {
		return false, err
	}
	return count <= reactionActionsPerMinute, nil
}

func (l *ValkeyReactionLimiter) Close() { l.client.Close() }
