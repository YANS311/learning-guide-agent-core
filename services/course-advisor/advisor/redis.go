package advisor

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/rand/v2"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

type RedisLab struct {
	client  *redis.Client
	secret  []byte
	metrics *Metrics
}

func NewRedisLab(addr, password, marker string, m *Metrics) (*RedisLab, error) {
	if !labAddress(addr, "redis", 6379) || !shaRE.MatchString(marker) || password == "" {
		return nil, Invalid
	}
	return &RedisLab{redis.NewClient(&redis.Options{
		Addr: addr, Password: password, PoolSize: 8, MaxRetries: -1, DialTimeout: 250 * time.Millisecond,
		ReadTimeout: 250 * time.Millisecond, WriteTimeout: 250 * time.Millisecond, PoolTimeout: 300 * time.Millisecond,
		ContextTimeoutEnabled: true,
	}), []byte(marker), m}, nil
}

func (r *RedisLab) Close() error { return r.client.Close() }

// The expiry is set with the first increment in the same atomic operation.
var rateWindow = redis.NewScript(`
local value = redis.call("INCR", KEYS[1])
if value == 1 then redis.call("PEXPIRE", KEYS[1], ARGV[1]) end
return value
`)

func (r *RedisLab) Admit(ctx context.Context, owner string) error {
	ctx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer cancel()
	key := "advisor:p6:rate:" + digest(append(append([]byte(nil), r.secret...), owner...))
	n, err := rateWindow.Run(ctx, r.client, []string{key}, 10000).Int()
	if err != nil {
		if r.metrics != nil {
			r.metrics.redisErrors.Add(1)
		}
		return RateUnavailable
	}
	if n > 20 {
		return Busy
	}
	return nil
}

type CachedGraph struct {
	Graph
	Cache  *RedisLab
	TTL    time.Duration
	flight singleflight.Group
}

type signedCheck struct {
	Proof CourseCheck `json:"proof"`
	MAC   string      `json:"mac"`
}

func (c *CachedGraph) sign(key string, p CourseCheck) string {
	data, _ := json.Marshal(p)
	h := hmac.New(sha256.New, c.Cache.secret)
	h.Write([]byte(key))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

func (c *CachedGraph) cached(ctx context.Context, key, version string) (CourseCheck, bool) {
	var found signedCheck
	text, err := c.Cache.client.Get(ctx, key).Bytes()
	if err != nil && err != redis.Nil && c.Cache.metrics != nil {
		c.Cache.metrics.redisErrors.Add(1)
	}
	if err != nil || len(text) > 12*1024 || json.Unmarshal(text, &found) != nil ||
		found.Proof.Version != version || !idsOK(found.Proof.Required, 100) ||
		!hmac.Equal([]byte(found.MAC), []byte(c.sign(key, found.Proof))) {
		return CourseCheck{}, false
	}
	return found.Proof, true
}

func (c *CachedGraph) CheckVersion(ctx context.Context, id, version string) (CourseCheck, error) {
	if !idRE.MatchString(id) || !shaRE.MatchString(version) {
		return CourseCheck{}, Invalid
	}
	key := "advisor:p6:prereq:" + Rules + ":" + version + ":" + id
	if p, ok := c.cached(ctx, key, version); ok {
		if c.Cache.metrics != nil {
			c.Cache.metrics.cacheHits.Add(1)
		}
		return p, nil
	}
	if c.Cache.metrics != nil {
		c.Cache.metrics.cacheMisses.Add(1)
	}
	result := c.flight.DoChan(key, func() (any, error) {
		job, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if p, ok := c.cached(job, key, version); ok {
			return p, nil
		}
		p, err := c.Graph.Check(job, id)
		if err != nil {
			return nil, err
		}
		if p.Version != version {
			return nil, Changed
		}
		if !idsOK(p.Required, 100) {
			return nil, Dependency
		}
		blob, err := json.Marshal(signedCheck{p, c.sign(key, p)})
		ttl := c.TTL
		if ttl <= 0 || ttl > time.Minute {
			ttl = 35 * time.Second
		}
		ttl += time.Duration(rand.IntN(250)) * time.Millisecond
		if err == nil {
			if err = c.Cache.client.Set(job, key, blob, ttl).Err(); err != nil && c.Cache.metrics != nil {
				c.Cache.metrics.redisErrors.Add(1)
			}
		}
		return p, nil
	})
	select {
	case <-ctx.Done():
		return CourseCheck{}, ctx.Err()
	case r := <-result:
		if r.Err != nil {
			return CourseCheck{}, r.Err
		}
		return r.Val.(CourseCheck), nil
	}
}
