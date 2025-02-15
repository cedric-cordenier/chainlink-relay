package internal

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"time"

	"github.com/mwitkow/grpc-proxy/proxy"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/smartcontractkit/libocr/commontypes"
	"github.com/smartcontractkit/libocr/offchainreporting2/reportingplugin/median"
	libocr "github.com/smartcontractkit/libocr/offchainreporting2plus/types"

	"github.com/smartcontractkit/chainlink-relay/pkg/logger"
	"github.com/smartcontractkit/chainlink-relay/pkg/loop/internal/pb"
	"github.com/smartcontractkit/chainlink-relay/pkg/types"
)

var _ types.PluginMedian = (*PluginMedianClient)(nil)

type PluginMedianClient struct {
	*pluginClient
	*serviceClient

	median pb.PluginMedianClient
}

func NewPluginMedianClient(broker Broker, brokerCfg BrokerConfig, conn *grpc.ClientConn) *PluginMedianClient {
	brokerCfg.Logger = logger.Named(brokerCfg.Logger, "PluginMedianClient")
	pc := newPluginClient(broker, brokerCfg, conn)
	return &PluginMedianClient{pluginClient: pc, median: pb.NewPluginMedianClient(pc), serviceClient: newServiceClient(pc.brokerExt, pc)}
}

func (m *PluginMedianClient) NewMedianFactory(ctx context.Context, provider types.MedianProvider, dataSource, juelsPerFeeCoin median.DataSource, errorLog types.ErrorLog) (types.ReportingPluginFactory, error) {
	cc := m.newClientConn("MedianPluginFactory", func(ctx context.Context) (id uint32, deps resources, err error) {
		dataSourceID, dsRes, err := m.serveNew("DataSource", func(s *grpc.Server) {
			pb.RegisterDataSourceServer(s, &dataSourceServer{impl: dataSource})
		})
		if err != nil {
			return 0, nil, err
		}
		deps.Add(dsRes)

		juelsPerFeeCoinDataSourceID, juelsPerFeeCoinDataSourceRes, err := m.serveNew("JuelsPerFeeCoinDataSource", func(s *grpc.Server) {
			pb.RegisterDataSourceServer(s, &dataSourceServer{impl: juelsPerFeeCoin})
		})
		if err != nil {
			return 0, nil, err
		}
		deps.Add(juelsPerFeeCoinDataSourceRes)

		var (
			providerID  uint32
			providerRes resource
		)
		if grpcProvider, ok := provider.(GRPCClientConn); ok {
			providerID, providerRes, err = m.serve("MedianProvider", proxy.NewProxy(grpcProvider.ClientConn()))
		} else {
			providerID, providerRes, err = m.serveNew("MedianProvider", func(s *grpc.Server) {
				pb.RegisterServiceServer(s, &serviceServer{srv: provider})
				pb.RegisterOffchainConfigDigesterServer(s, &offchainConfigDigesterServer{impl: provider.OffchainConfigDigester()})
				pb.RegisterContractConfigTrackerServer(s, &contractConfigTrackerServer{impl: provider.ContractConfigTracker()})
				pb.RegisterContractTransmitterServer(s, &contractTransmitterServer{impl: provider.ContractTransmitter()})
				pb.RegisterReportCodecServer(s, &reportCodecServer{impl: provider.ReportCodec()})
				pb.RegisterMedianContractServer(s, &medianContractServer{impl: provider.MedianContract()})
				pb.RegisterOnchainConfigCodecServer(s, &onchainConfigCodecServer{impl: provider.OnchainConfigCodec()})
			})
		}
		if err != nil {
			return 0, nil, err
		}
		deps.Add(providerRes)

		errorLogID, errorLogRes, err := m.serveNew("ErrorLog", func(s *grpc.Server) {
			pb.RegisterErrorLogServer(s, &errorLogServer{impl: errorLog})
		})
		if err != nil {
			return 0, nil, err
		}
		deps.Add(errorLogRes)

		reply, err := m.median.NewMedianFactory(ctx, &pb.NewMedianFactoryRequest{
			MedianProviderID:            providerID,
			DataSourceID:                dataSourceID,
			JuelsPerFeeCoinDataSourceID: juelsPerFeeCoinDataSourceID,
			ErrorLogID:                  errorLogID,
		})
		if err != nil {
			return 0, nil, err
		}
		return reply.ReportingPluginFactoryID, nil, nil
	})
	return newReportingPluginFactoryClient(m.pluginClient.brokerExt, cc), nil
}

var _ pb.PluginMedianServer = (*pluginMedianServer)(nil)

type pluginMedianServer struct {
	pb.UnimplementedPluginMedianServer

	*brokerExt
	impl types.PluginMedian
}

func RegisterPluginMedianServer(server *grpc.Server, broker Broker, brokerCfg BrokerConfig, impl types.PluginMedian) error {
	pb.RegisterPluginMedianServer(server, newPluginMedianServer(&brokerExt{broker, brokerCfg}, impl))
	return nil
}

func newPluginMedianServer(b *brokerExt, mp types.PluginMedian) *pluginMedianServer {
	return &pluginMedianServer{brokerExt: b.withName("PluginMedian"), impl: mp}
}

func (m *pluginMedianServer) NewMedianFactory(ctx context.Context, request *pb.NewMedianFactoryRequest) (*pb.NewMedianFactoryReply, error) {
	dsConn, err := m.dial(request.DataSourceID)
	if err != nil {
		return nil, ErrConnDial{Name: "DataSource", ID: request.DataSourceID, Err: err}
	}
	dsRes := resource{dsConn, "DataSource"}
	dataSource := newDataSourceClient(dsConn)

	juelsConn, err := m.dial(request.JuelsPerFeeCoinDataSourceID)
	if err != nil {
		m.closeAll(dsRes)
		return nil, ErrConnDial{Name: "JuelsPerFeeCoinDataSource", ID: request.JuelsPerFeeCoinDataSourceID, Err: err}
	}
	juelsRes := resource{juelsConn, "JuelsPerFeeCoinDataSource"}
	juelsPerFeeCoin := newDataSourceClient(juelsConn)

	providerConn, err := m.dial(request.MedianProviderID)
	if err != nil {
		m.closeAll(dsRes, juelsRes)
		return nil, ErrConnDial{Name: "MedianProvider", ID: request.MedianProviderID, Err: err}
	}
	providerRes := resource{providerConn, "MedianProvider"}
	provider := newMedianProviderClient(m.brokerExt, providerConn)

	errorLogConn, err := m.dial(request.ErrorLogID)
	if err != nil {
		m.closeAll(dsRes, juelsRes, providerRes)
		return nil, ErrConnDial{Name: "ErrorLog", ID: request.ErrorLogID, Err: err}
	}
	errorLogRes := resource{errorLogConn, "ErrorLog"}
	errorLog := newErrorLogClient(errorLogConn)

	factory, err := m.impl.NewMedianFactory(ctx, provider, dataSource, juelsPerFeeCoin, errorLog)
	if err != nil {
		m.closeAll(dsRes, juelsRes, providerRes, errorLogRes)
		return nil, err
	}

	id, _, err := m.serveNew("ReportingPluginProvider", func(s *grpc.Server) {
		pb.RegisterServiceServer(s, &serviceServer{srv: factory})
		pb.RegisterReportingPluginFactoryServer(s, newReportingPluginFactoryServer(factory, m.brokerExt))
	}, dsRes, juelsRes, providerRes, errorLogRes)
	if err != nil {
		return nil, err
	}

	return &pb.NewMedianFactoryReply{ReportingPluginFactoryID: id}, nil
}

var (
	_ types.MedianProvider = (*medianProviderClient)(nil)
	_ GRPCClientConn       = (*medianProviderClient)(nil)
)

type medianProviderClient struct {
	*configProviderClient
	contractTransmitter libocr.ContractTransmitter
	reportCodec         median.ReportCodec
	medianContract      median.MedianContract
	onchainConfigCodec  median.OnchainConfigCodec
}

func (m *medianProviderClient) ClientConn() grpc.ClientConnInterface { return m.cc }

func newMedianProviderClient(b *brokerExt, cc grpc.ClientConnInterface) *medianProviderClient {
	m := &medianProviderClient{configProviderClient: newConfigProviderClient(b.withName("MedianProviderClient"), cc)}
	m.contractTransmitter = &contractTransmitterClient{b, pb.NewContractTransmitterClient(m.cc)}
	m.reportCodec = &reportCodecClient{b, pb.NewReportCodecClient(m.cc)}
	m.medianContract = &medianContractClient{pb.NewMedianContractClient(m.cc)}
	m.onchainConfigCodec = &onchainConfigCodecClient{b, pb.NewOnchainConfigCodecClient(m.cc)}
	return m
}

func (m *medianProviderClient) ContractTransmitter() libocr.ContractTransmitter {
	return m.contractTransmitter
}

func (m *medianProviderClient) ReportCodec() median.ReportCodec {
	return m.reportCodec
}

func (m *medianProviderClient) MedianContract() median.MedianContract {
	return m.medianContract
}

func (m *medianProviderClient) OnchainConfigCodec() median.OnchainConfigCodec {
	return m.onchainConfigCodec
}

var _ median.ReportCodec = (*reportCodecClient)(nil)

type reportCodecClient struct {
	*brokerExt
	grpc pb.ReportCodecClient
}

func (r *reportCodecClient) BuildReport(observations []median.ParsedAttributedObservation) (report libocr.Report, err error) {
	ctx, cancel := r.stopCtx()
	defer cancel()

	var req pb.BuildReportRequest
	for _, o := range observations {
		req.Observations = append(req.Observations, &pb.ParsedAttributedObservation{
			Timestamp:       o.Timestamp,
			Value:           pb.NewBigIntFromInt(o.Value),
			JulesPerFeeCoin: pb.NewBigIntFromInt(o.JuelsPerFeeCoin),
			Observer:        uint32(o.Observer),
		})
	}
	var reply *pb.BuildReportReply
	reply, err = r.grpc.BuildReport(ctx, &req)
	if err != nil {
		return
	}
	report = reply.Report
	return
}

func (r *reportCodecClient) MedianFromReport(report libocr.Report) (*big.Int, error) {
	ctx, cancel := r.stopCtx()
	defer cancel()

	reply, err := r.grpc.MedianFromReport(ctx, &pb.MedianFromReportRequest{Report: report})
	if err != nil {
		return nil, err
	}
	return reply.Median.Int(), nil
}

func (r *reportCodecClient) MaxReportLength(n int) (int, error) {
	ctx, cancel := r.stopCtx()
	defer cancel()

	reply, err := r.grpc.MaxReportLength(ctx, &pb.MaxReportLengthRequest{N: int64(n)})
	if err != nil {
		return -1, err
	}
	return int(reply.Max), nil
}

var _ pb.ReportCodecServer = (*reportCodecServer)(nil)

type reportCodecServer struct {
	pb.UnimplementedReportCodecServer
	impl median.ReportCodec
}

func (r *reportCodecServer) BuildReport(ctx context.Context, request *pb.BuildReportRequest) (*pb.BuildReportReply, error) {
	var obs []median.ParsedAttributedObservation
	for _, o := range request.Observations {

		val, jpfc := o.Value.Int(), o.JulesPerFeeCoin.Int()
		if o.Observer > math.MaxUint8 {
			return nil, fmt.Errorf("expected uint8 Observer (max %d) but got %d", math.MaxUint8, o.Observer)
		}
		obs = append(obs, median.ParsedAttributedObservation{
			Timestamp:       o.Timestamp,
			Value:           val,
			JuelsPerFeeCoin: jpfc,
			Observer:        commontypes.OracleID(o.Observer),
		})
	}
	report, err := r.impl.BuildReport(obs)
	if err != nil {
		return nil, err
	}
	return &pb.BuildReportReply{Report: report}, nil
}

func (r *reportCodecServer) MedianFromReport(ctx context.Context, request *pb.MedianFromReportRequest) (*pb.MedianFromReportReply, error) {
	m, err := r.impl.MedianFromReport(request.Report)
	if err != nil {
		return nil, err
	}
	return &pb.MedianFromReportReply{Median: pb.NewBigIntFromInt(m)}, nil
}

func (r *reportCodecServer) MaxReportLength(ctx context.Context, request *pb.MaxReportLengthRequest) (*pb.MaxReportLengthReply, error) {
	l, err := r.impl.MaxReportLength(int(request.N))
	if err != nil {
		return nil, err
	}
	return &pb.MaxReportLengthReply{Max: int64(l)}, nil
}

var _ median.MedianContract = (*medianContractClient)(nil)

type medianContractClient struct {
	grpc pb.MedianContractClient
}

func (m *medianContractClient) LatestTransmissionDetails(ctx context.Context) (configDigest libocr.ConfigDigest, epoch uint32, round uint8, latestAnswer *big.Int, latestTimestamp time.Time, err error) {
	var reply *pb.LatestTransmissionDetailsReply
	reply, err = m.grpc.LatestTransmissionDetails(ctx, &pb.LatestTransmissionDetailsRequest{})
	if err != nil {
		return
	}
	if l := len(reply.ConfigDigest); l != 32 {
		err = fmt.Errorf("expected ConfigDigest length 32 but got %d", l)
		return
	}
	copy(configDigest[:], reply.ConfigDigest)
	epoch = reply.Epoch
	if reply.Round > math.MaxUint8 {
		err = fmt.Errorf("expected uint8 Round (max %d) but got %d", math.MaxUint8, reply.Round)
		return
	}
	round = uint8(reply.Round)
	latestAnswer = reply.LatestAnswer.Int()
	latestTimestamp = reply.LatestTimestamp.AsTime()
	return
}

func (m *medianContractClient) LatestRoundRequested(ctx context.Context, lookback time.Duration) (configDigest libocr.ConfigDigest, epoch uint32, round uint8, err error) {
	reply, err := m.grpc.LatestRoundRequested(ctx, &pb.LatestRoundRequestedRequest{Lookback: int64(lookback)})
	if err != nil {
		return
	}
	if l := len(reply.ConfigDigest); l != 32 {
		err = fmt.Errorf("expected ConfigDigest length 32 but got %d", l)
		return
	}
	copy(configDigest[:], reply.ConfigDigest)
	epoch = reply.Epoch
	if reply.Round > math.MaxUint8 {
		err = fmt.Errorf("expected uint8 Round (max %d) but got %d", math.MaxUint8, reply.Round)
		return
	}
	round = uint8(reply.Round)
	return
}

var _ pb.MedianContractServer = (*medianContractServer)(nil)

type medianContractServer struct {
	pb.UnimplementedMedianContractServer
	impl median.MedianContract
}

func (m *medianContractServer) LatestTransmissionDetails(ctx context.Context, _ *pb.LatestTransmissionDetailsRequest) (*pb.LatestTransmissionDetailsReply, error) {
	digest, epoch, round, latestAnswer, latestTimestamp, err := m.impl.LatestTransmissionDetails(ctx)
	if err != nil {
		return nil, err
	}

	return &pb.LatestTransmissionDetailsReply{
		ConfigDigest:    digest[:],
		Epoch:           epoch,
		Round:           uint32(round),
		LatestAnswer:    pb.NewBigIntFromInt(latestAnswer),
		LatestTimestamp: timestamppb.New(latestTimestamp),
	}, nil
}

func (m *medianContractServer) LatestRoundRequested(ctx context.Context, request *pb.LatestRoundRequestedRequest) (*pb.LatestRoundRequestedReply, error) {
	digest, epoch, round, err := m.impl.LatestRoundRequested(ctx, time.Duration(request.Lookback))
	if err != nil {
		return nil, err
	}

	return &pb.LatestRoundRequestedReply{
		ConfigDigest: digest[:],
		Epoch:        epoch,
		Round:        uint32(round),
	}, nil
}

var _ median.OnchainConfigCodec = (*onchainConfigCodecClient)(nil)

type onchainConfigCodecClient struct {
	*brokerExt
	grpc pb.OnchainConfigCodecClient
}

func (o *onchainConfigCodecClient) Encode(config median.OnchainConfig) ([]byte, error) {
	ctx, cancel := o.stopCtx()
	defer cancel()

	req := &pb.EncodeRequest{OnchainConfig: &pb.OnchainConfig{
		Min: pb.NewBigIntFromInt(config.Min),
		Max: pb.NewBigIntFromInt(config.Max),
	}}
	reply, err := o.grpc.Encode(ctx, req)
	if err != nil {
		return nil, err
	}
	return reply.Encoded, nil
}

func (o *onchainConfigCodecClient) Decode(bytes []byte) (oc median.OnchainConfig, err error) {
	ctx, cancel := o.stopCtx()
	defer cancel()

	var reply *pb.DecodeReply
	reply, err = o.grpc.Decode(ctx, &pb.DecodeRequest{Encoded: bytes})
	if err != nil {
		return
	}
	oc.Min, oc.Max = reply.OnchainConfig.Min.Int(), reply.OnchainConfig.Max.Int()
	return
}

var _ pb.OnchainConfigCodecServer = (*onchainConfigCodecServer)(nil)

type onchainConfigCodecServer struct {
	pb.UnimplementedOnchainConfigCodecServer
	impl median.OnchainConfigCodec
}

func (o *onchainConfigCodecServer) Encode(ctx context.Context, request *pb.EncodeRequest) (*pb.EncodeReply, error) {
	min, max := request.OnchainConfig.Min.Int(), request.OnchainConfig.Max.Int()
	b, err := o.impl.Encode(median.OnchainConfig{Max: max, Min: min})
	if err != nil {
		return nil, err
	}
	return &pb.EncodeReply{Encoded: b}, nil
}

func (o *onchainConfigCodecServer) Decode(ctx context.Context, request *pb.DecodeRequest) (*pb.DecodeReply, error) {
	oc, err := o.impl.Decode(request.Encoded)
	if err != nil {
		return nil, err
	}
	return &pb.DecodeReply{OnchainConfig: &pb.OnchainConfig{
		Min: pb.NewBigIntFromInt(oc.Min),
		Max: pb.NewBigIntFromInt(oc.Max),
	}}, nil
}
