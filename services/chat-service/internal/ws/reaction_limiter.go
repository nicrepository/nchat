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

// ponytail: INCR+conditional EXPIRE has no atomic native equivalent in
// valkey-go (or Redis) short of Lua — two round trips would race between the
// INCR and the EXPIRE. Reviewed for v2; kept as is, no simpler swap available.
var actionRateScript = valkey.NewLuaScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then redis.call('EXPIRE', KEYS[1], ARGV[1]) end
return count`)

type ValkeyReactionLimiter struct {
	client        valkey.Client
	maxActions    int
	windowSeconds int
}

func NewValkeyReactionLimiter(valkeyURL string, maxActions, windowSeconds int) (*ValkeyReactionLimiter, error) {
	if valkeyURL == "" {
		return nil, errors.New("reaction limiter requires VALKEY_URL")
	}
	if maxActions <= 0 || windowSeconds <= 0 {
		return nil, errors.New("reaction limiter requires positive limit and window")
	}
	option, err := valkey.ParseURL(valkeyURL)
	if err != nil {
		return nil, fmt.Errorf("parse reaction limiter valkey URL: %w", err)
	}
	client, err := valkey.NewClient(option)
	if err != nil {
		return nil, fmt.Errorf("create reaction limiter client: %w", err)
	}
	return &ValkeyReactionLimiter{client: client, maxActions: maxActions, windowSeconds: windowSeconds}, nil
}

func (l *ValkeyReactionLimiter) Allow(ctx context.Context, userID string) (bool, error) {
	return l.AllowAction(ctx, userID, "reaction")
}

// AllowAction applies the shared Lua fixed-window limiter to a named user action.
func (l *ValkeyReactionLimiter) AllowAction(ctx context.Context, userID, action string) (bool, error) {
	return l.AllowActionWithLimit(ctx, userID, action, l.maxActions, l.windowSeconds)
}

// AllowActionWithLimit applies the shared limiter with an operation-specific budget.
func (l *ValkeyReactionLimiter) AllowActionWithLimit(ctx context.Context, userID, action string, maxActions, windowSeconds int) (bool, error) {
	if maxActions <= 0 || windowSeconds <= 0 {
		return false, errors.New("action limiter requires positive limit and window")
	}
	sum := sha256.Sum256([]byte(userID))
	key := "nchat:chat:action:" + action + ":" + hex.EncodeToString(sum[:])
	count, err := actionRateScript.Exec(ctx, l.client, []string{key}, []string{strconv.Itoa(windowSeconds)}).AsInt64()
	if err != nil {
		return false, err
	}
	return count <= int64(maxActions), nil
}

func (l *ValkeyReactionLimiter) Close() { l.client.Close() }
