package loop_test

import (
	"context"
	"testing"
	"time"

	"github.com/hashicorp/go-plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-relay/pkg/logger"
	"github.com/smartcontractkit/chainlink-relay/pkg/loop"
	"github.com/smartcontractkit/chainlink-relay/pkg/loop/internal/test"
	"github.com/smartcontractkit/chainlink-relay/pkg/types"
)

func TestPluginMedian(t *testing.T) {
	t.Parallel()

	stopCh := newStopCh(t)
	testPlugin(t, loop.PluginMedianName, &loop.GRPCPluginMedian{PluginServer: test.StaticPluginMedian{}, BrokerConfig: loop.BrokerConfig{Logger: logger.Test(t), StopCh: stopCh}}, test.TestPluginMedian)

	t.Run("proxy", func(t *testing.T) {
		testPlugin(t, loop.PluginRelayerName, &loop.GRPCPluginRelayer{PluginServer: test.StaticPluginRelayer{}, BrokerConfig: loop.BrokerConfig{Logger: logger.Test(t), StopCh: stopCh}}, func(t *testing.T, pr loop.PluginRelayer) {
			p := newMedianProvider(t, pr)
			pm := test.PluginMedianTest{MedianProvider: p}
			testPlugin(t, loop.PluginMedianName, &loop.GRPCPluginMedian{PluginServer: test.StaticPluginMedian{}, BrokerConfig: loop.BrokerConfig{Logger: logger.Test(t), StopCh: stopCh}}, pm.TestPluginMedian)
		})
	})
}

func TestPluginMedianExec(t *testing.T) {
	t.Parallel()
	stopCh := newStopCh(t)
	median := loop.GRPCPluginMedian{BrokerConfig: loop.BrokerConfig{Logger: logger.Test(t), StopCh: stopCh}}
	cc := median.ClientConfig()
	cc.Cmd = helperProcess(loop.PluginMedianName)
	c := plugin.NewClient(cc)
	t.Cleanup(c.Kill)
	client, err := c.Client()
	require.NoError(t, err)
	defer client.Close()
	require.NoError(t, client.Ping())
	i, err := client.Dispense(loop.PluginMedianName)
	require.NoError(t, err)

	test.TestPluginMedian(t, i.(types.PluginMedian))

	t.Run("proxy", func(t *testing.T) {
		pr := newPluginRelayerExec(t, stopCh)
		p := newMedianProvider(t, pr)
		pm := test.PluginMedianTest{MedianProvider: p}
		pm.TestPluginMedian(t, i.(types.PluginMedian))
	})
}

func newStopCh(t *testing.T) <-chan struct{} {
	stopCh := make(chan struct{})
	if d, ok := t.Deadline(); ok {
		time.AfterFunc(time.Until(d), func() { close(stopCh) })
	}
	return stopCh
}

func newMedianProvider(t *testing.T, pr loop.PluginRelayer) types.MedianProvider {
	ctx := context.Background()
	r, err := pr.NewRelayer(ctx, test.ConfigTOML, test.StaticKeystore{})
	require.NoError(t, err)
	require.NoError(t, r.Start(ctx))
	t.Cleanup(func() { assert.NoError(t, r.Close()) })
	p, err := r.NewMedianProvider(ctx, test.RelayArgs, test.PluginArgs)
	require.NoError(t, err)
	require.NoError(t, p.Start(ctx))
	t.Cleanup(func() { assert.NoError(t, p.Close()) })
	return p
}
