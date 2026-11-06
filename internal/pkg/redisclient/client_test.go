package redisclient_test

import (
	"testing"
	"time"

	"backend-go/internal/pkg/redisclient"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestNewSingleClient(t *testing.T) {
	client, err := redisclient.New(redisclient.ModeSingle, []string{"localhost:6379"}, "secret", 2)
	require.NoError(t, err)
	require.IsType(t, (*redis.Client)(nil), client)
	require.NoError(t, client.Close())
}

func TestNewClusterClient(t *testing.T) {
	client, err := redisclient.New(redisclient.ModeCluster, []string{"redis-0:6379", "redis-1:6379"}, "secret", 0)
	require.NoError(t, err)
	require.IsType(t, (*redis.ClusterClient)(nil), client)
	require.NoError(t, client.Close())
}

func TestNewClusterClientRejectsLogicalDatabase(t *testing.T) {
	_, err := redisclient.New(redisclient.ModeCluster, []string{"redis-0:6379"}, "", 1)
	require.Error(t, err)
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	_, err := redisclient.New(redisclient.ModeSingle, []string{"redis-0:6379", "redis-1:6379"}, "", 0)
	require.Error(t, err)

	_, err = redisclient.New("bogus", []string{"redis-0:6379"}, "", 0)
	require.Error(t, err)
}

func TestSentinelRequiresMasterName(t *testing.T) {
	_, err := redisclient.NewClient(redisclient.Options{Mode: redisclient.ModeSentinel, Addresses: []string{"s1:26379"}})
	require.Error(t, err)

	client, err := redisclient.NewClient(redisclient.Options{Mode: redisclient.ModeSentinel, Addresses: []string{"s1:26379"}, MasterName: "mymaster"})
	require.NoError(t, err)
	require.NoError(t, client.Close())
}

func TestTLSAndTimeoutsApplied(t *testing.T) {
	client, err := redisclient.NewClient(redisclient.Options{
		Mode: redisclient.ModeSingle, Addresses: []string{"r:6379"}, Username: "app", TLS: true,
		ReadTimeout: 250 * time.Millisecond, PoolSize: 7,
	})
	require.NoError(t, err)
	opts := client.(*redis.Client).Options()
	require.NotNil(t, opts.TLSConfig)
	require.Equal(t, "app", opts.Username)
	require.Equal(t, 250*time.Millisecond, opts.ReadTimeout)
	require.Equal(t, 7, opts.PoolSize)
	require.NoError(t, client.Close())
}
