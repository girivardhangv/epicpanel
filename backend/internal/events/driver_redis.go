package events

import (
	"context"

	"github.com/redis/go-redis/v9"
)

// RedisDriver delivers events via Redis pub/sub (EPICPANEL_REDIS_URL).
type RedisDriver struct {
	client *redis.Client
}

func NewRedisDriver(ctx context.Context, redisURL string) (*RedisDriver, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, err
	}
	client := redis.NewClient(opts)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, err
	}
	return &RedisDriver{client: client}, nil
}

func (d *RedisDriver) Publish(ctx context.Context, payload []byte) error {
	return d.client.Publish(ctx, Channel, payload).Err()
}

func (d *RedisDriver) Subscribe(ctx context.Context, onEvent Handler) {
	sub := d.client.Subscribe(ctx, Channel)
	defer sub.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-sub.Channel():
			if !ok {
				return
			}
			decodeAndDispatch(onEvent, []byte(msg.Payload))
		}
	}
}

func (d *RedisDriver) Close(_ context.Context) {
	_ = d.client.Close()
}
