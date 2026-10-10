package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"github.com/dingtalk-fde/eino-employee-loop/core"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

func newToken() string { b := make([]byte, 8); _, _ = rand.Read(b); return hex.EncodeToString(b) }

// ---------------- in-process core.KV ----------------
type MemKV struct {
	mu sync.Mutex
	m  map[string]memEnt
}
type memEnt struct {
	v   string
	exp time.Time
}

func NewMemKV() *MemKV { return &MemKV{m: map[string]memEnt{}} }
func (k *MemKV) live(key string) (memEnt, bool) {
	e, ok := k.m[key]
	if ok && !e.exp.IsZero() && time.Now().After(e.exp) {
		delete(k.m, key)
		return memEnt{}, false
	}
	return e, ok
}
func exp(ttl time.Duration) time.Time {
	if ttl <= 0 {
		return time.Time{}
	}
	return time.Now().Add(ttl)
}
func (k *MemKV) Get(_ context.Context, key string) (string, bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	e, ok := k.live(key)
	return e.v, ok, nil
}
func (k *MemKV) Set(_ context.Context, key, val string, ttl time.Duration) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.m[key] = memEnt{val, exp(ttl)}
	return nil
}
func (k *MemKV) SetNX(_ context.Context, key, val string, ttl time.Duration) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, ok := k.live(key); ok {
		return false, nil
	}
	k.m[key] = memEnt{val, exp(ttl)}
	return true, nil
}
func (k *MemKV) Del(_ context.Context, key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.m, key)
	return nil
}
func (k *MemKV) Lock(ctx context.Context, key string, ttl time.Duration) (string, bool, error) {
	t := newToken()
	ok, err := k.SetNX(ctx, "lock:"+key, t, ttl)
	return t, ok, err
}
func (k *MemKV) Unlock(_ context.Context, key, token string) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if e, ok := k.live("lock:" + key); ok && e.v == token {
		delete(k.m, "lock:"+key)
		return true, nil
	}
	return false, nil
}
func (k *MemKV) Incr(_ context.Context, key string, ttl time.Duration) (int64, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	e, ok := k.live(key)
	n, _ := strconv.ParseInt(e.v, 10, 64)
	n++
	if !ok {
		e.exp = exp(ttl)
	}
	k.m[key] = memEnt{strconv.FormatInt(n, 10), e.exp}
	return n, nil
}
func (k *MemKV) Close() error { return nil }

// ---------------- Redis core.KV (cloud shape) ----------------
type RedisKV struct {
	c      *redis.Client
	prefix string
}

func NewRedisKV(addr, prefix string) *RedisKV {
	return &RedisKV{c: redis.NewClient(&redis.Options{Addr: addr}), prefix: prefix}
}
func (r *RedisKV) k(s string) string { return r.prefix + s }
func (r *RedisKV) Get(ctx context.Context, key string) (string, bool, error) {
	v, err := r.c.Get(ctx, r.k(key)).Result()
	if err == redis.Nil {
		return "", false, nil
	}
	return v, err == nil, err
}
func (r *RedisKV) Set(ctx context.Context, key, val string, ttl time.Duration) error {
	return r.c.Set(ctx, r.k(key), val, ttl).Err()
}
func (r *RedisKV) SetNX(ctx context.Context, key, val string, ttl time.Duration) (bool, error) {
	return r.c.SetNX(ctx, r.k(key), val, ttl).Result()
}
func (r *RedisKV) Del(ctx context.Context, key string) error { return r.c.Del(ctx, r.k(key)).Err() }
func (r *RedisKV) Lock(ctx context.Context, key string, ttl time.Duration) (string, bool, error) {
	t := newToken()
	ok, err := r.c.SetNX(ctx, r.k("lock:"+key), t, ttl).Result()
	return t, ok, err
}

var unlockLua = redis.NewScript(`if redis.call('get',KEYS[1])==ARGV[1] then return redis.call('del',KEYS[1]) else return 0 end`)

func (r *RedisKV) Unlock(ctx context.Context, key, token string) (bool, error) {
	n, err := unlockLua.Run(ctx, r.c, []string{r.k("lock:" + key)}, token).Int()
	return n == 1, err
}

var incrLua = redis.NewScript(`local n=redis.call('incr',KEYS[1]); if n==1 and tonumber(ARGV[1])>0 then redis.call('pexpire',KEYS[1],ARGV[1]) end return n`)

func (r *RedisKV) Incr(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	return incrLua.Run(ctx, r.c, []string{r.k(key)}, ttl.Milliseconds()).Int64()
}
func (r *RedisKV) Close() error { return r.c.Close() }

var (
	_ core.KV = (*MemKV)(nil)
	_ core.KV = (*RedisKV)(nil)
)
