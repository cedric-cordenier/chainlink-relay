package loop

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/smartcontractkit/libocr/offchainreporting2/reportingplugin/median"
	ocrtypes "github.com/smartcontractkit/libocr/offchainreporting2plus/types"

	"github.com/smartcontractkit/chainlink-relay/pkg/logger"
	"github.com/smartcontractkit/chainlink-relay/pkg/types"
	"github.com/smartcontractkit/chainlink-relay/pkg/utils"
)

var _ ocrtypes.ReportingPluginFactory = (*MedianService)(nil)

// MedianService is a [types.Service] that maintains an internal [types.PluginMedian].
type MedianService struct {
	pluginService[*GRPCPluginMedian, types.ReportingPluginFactory]
}

// NewMedianService returns a new [*MedianService].
// cmd must return a new exec.Cmd each time it is called.
func NewMedianService(lggr logger.Logger, grpcOpts GRPCOpts, cmd func() *exec.Cmd, provider types.MedianProvider, dataSource, juelsPerFeeCoin median.DataSource, errorLog types.ErrorLog) *MedianService {
	newService := func(ctx context.Context, instance any) (types.ReportingPluginFactory, error) {
		plug, ok := instance.(types.PluginMedian)
		if !ok {
			return nil, fmt.Errorf("expected PluginMedian but got %T", instance)
		}
		return plug.NewMedianFactory(ctx, provider, dataSource, juelsPerFeeCoin, errorLog)
	}
	stopCh := make(chan struct{})
	lggr = logger.Named(lggr, "MedianService")
	var ms MedianService
	broker := BrokerConfig{StopCh: stopCh, Logger: lggr, GRPCOpts: grpcOpts}
	ms.init(PluginMedianName, &GRPCPluginMedian{BrokerConfig: broker}, newService, lggr, cmd, stopCh)
	return &ms
}

func (m *MedianService) NewReportingPlugin(config ocrtypes.ReportingPluginConfig) (ocrtypes.ReportingPlugin, ocrtypes.ReportingPluginInfo, error) {
	ctx, cancel := utils.ContextFromChan(m.pluginService.stopCh)
	defer cancel()
	if err := m.wait(ctx); err != nil {
		return nil, ocrtypes.ReportingPluginInfo{}, err
	}
	return m.service.NewReportingPlugin(config)
}
