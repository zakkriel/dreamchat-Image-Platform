package ratelimit

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// NewRedisClient builds a go-redis client from the platform's existing Redis
// config (RedisAddr / RedisPassword), mirroring the asynq Redis wiring in
// internal/jobs. The caller owns the client lifecycle and must Close it on
// shutdown.
func NewRedisClient(addr, password string) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
	})
}

// incrementScript is the atomic "increment + create TTL on first increment"
// operation. INCR and PEXPIRE run in a single server-side Lua execution, so a
// connection drop can never land between them and leave a key with no
// expiry. The first increment (count == 1) creates the key's TTL; subsequent
// increments within the window leave the existing TTL untouched (fixed window).
var incrementScript = redis.NewScript(`
local count = redis.call("INCR", KEYS[1])
if count == 1 then
  redis.call("PEXPIRE", KEYS[1], ARGV[1])
end
return count
`)

// RedisStore is the production Store backed by a go-redis client.
type RedisStore struct {
	client *redis.Client
}

// NewRedisStore wraps a go-redis client as a Store.
func NewRedisStore(client *redis.Client) *RedisStore {
	return &RedisStore{client: client}
}

// Increment runs the atomic increment+expire Lua script and returns the
// post-increment count.
func (s *RedisStore) Increment(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	return incrementScript.Run(ctx, s.client, []string{key}, ttl.Milliseconds()).Int64()
}
