package monitoring

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/smartcontractkit/chainlink-relay/pkg/logger"
	"github.com/smartcontractkit/chainlink-relay/pkg/monitoring/config"
	"github.com/smartcontractkit/chainlink-relay/pkg/utils"
)

// Monitor is the entrypoint for an on-chain monitor integration.
// Monitors should only be created via NewMonitor()
type Monitor struct {
	RootContext context.Context

	ChainConfig ChainConfig
	Config      config.Config

	Log            Logger
	Producer       Producer
	Metrics        Metrics
	ChainMetrics   ChainMetrics
	SchemaRegistry SchemaRegistry

	SourceFactories   []SourceFactory
	ExporterFactories []ExporterFactory

	RDDSource Source
	RDDPoller Poller

	Manager Manager

	HTTPServer HTTPServer
}

// NewMonitor builds a new Monitor instance using dependency injection.
// If advanced configurations of the Monitor are required - for instance,
// adding a custom third party service to send data to - this method
// should provide a good starting template to do that.
func NewMonitor(
	rootCtx context.Context,
	log Logger,
	chainConfig ChainConfig,
	envelopeSourceFactory SourceFactory,
	txResultsSourceFactory SourceFactory,
	feedsParser FeedsParser,
	nodesParser NodesParser,
) (*Monitor, error) {
	cfg, err := config.Parse()
	if err != nil {
		return nil, fmt.Errorf("failed to parse generic configuration: %w", err)
	}

	metrics := NewMetrics(logger.With(log, "component", "metrics"))
	chainMetrics := NewChainMetrics(chainConfig)

	sourceFactories := []SourceFactory{envelopeSourceFactory, txResultsSourceFactory}

	producer, err := NewProducer(rootCtx, logger.With(log, "component", "producer"), cfg.Kafka)
	if err != nil {
		return nil, fmt.Errorf("failed to create kafka producer: %w", err)
	}
	producer = NewInstrumentedProducer(producer, chainMetrics)

	schemaRegistry := NewSchemaRegistry(cfg.SchemaRegistry, log)

	transmissionSchema, err := schemaRegistry.EnsureSchema(
		SubjectFromTopic(cfg.Kafka.TransmissionTopic), TransmissionAvroSchema)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare transmission schema: %w", err)
	}
	configSetSimplifiedSchema, err := schemaRegistry.EnsureSchema(
		SubjectFromTopic(cfg.Kafka.ConfigSetSimplifiedTopic), ConfigSetSimplifiedAvroSchema)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare config_set_simplified schema: %w", err)
	}

	prometheusExporterFactory := NewPrometheusExporterFactory(
		logger.With(log, "component", "prometheus-exporter"),
		metrics,
	)
	kafkaExporterFactory, err := NewKafkaExporterFactory(
		logger.With(log, "component", "kafka-exporter"),
		producer,
		[]Pipeline{
			{cfg.Kafka.TransmissionTopic, MakeTransmissionMapping, transmissionSchema},
			{cfg.Kafka.ConfigSetSimplifiedTopic, MakeConfigSetSimplifiedMapping, configSetSimplifiedSchema},
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create kafka exporter: %w", err)
	}

	exporterFactories := []ExporterFactory{prometheusExporterFactory, kafkaExporterFactory}

	rddSource := NewRDDSource(
		cfg.Feeds.URL, feedsParser, cfg.Feeds.IgnoreIDs,
		cfg.Nodes.URL, nodesParser,
		logger.With(log, "component", "rdd-source"),
	)

	rddPoller := NewSourcePoller(
		rddSource,
		logger.With(log, "component", "rdd-poller"),
		cfg.Feeds.RDDPollInterval,
		cfg.Feeds.RDDReadTimeout,
		0, // no buffering!
	)

	manager := NewManager(
		logger.With(log, "component", "manager"),
		rddPoller,
	)

	// Configure HTTP server
	httpServer := NewHTTPServer(rootCtx, cfg.HTTP.Address, logger.With(log, "component", "http-server"))
	httpServer.Handle("/metrics", metrics.HTTPHandler())
	httpServer.Handle("/debug", manager.HTTPHandler())
	// Required for k8s.
	httpServer.Handle("/health", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	return &Monitor{
		rootCtx,

		chainConfig,
		cfg,

		log,
		producer,
		metrics,
		chainMetrics,
		schemaRegistry,

		sourceFactories,
		exporterFactories,

		rddSource,
		rddPoller,

		manager,

		httpServer,
	}, nil
}

// Run() starts all the goroutines needed by a Monitor. The lifecycle of these routines
// is controlled by the context passed to the NewMonitor constructor.
func (m Monitor) Run() {
	rootCtx, cancel := context.WithCancel(m.RootContext)
	defer cancel()
	var subs utils.Subprocesses

	subs.Go(func() {
		m.RDDPoller.Run(rootCtx)
	})

	// Instrument all source factories
	instrumentedSourceFactories := []SourceFactory{}
	for _, factory := range m.SourceFactories {
		instrumentedSourceFactories = append(instrumentedSourceFactories,
			NewInstrumentedSourceFactory(factory, m.ChainMetrics))
	}

	monitor := NewMultiFeedMonitor(
		m.ChainConfig,
		m.Log,
		instrumentedSourceFactories,
		m.ExporterFactories,
		100, // bufferCapacity for source pollers
	)

	subs.Go(func() {
		m.Manager.Run(rootCtx, func(localCtx context.Context, data RDDData) {
			m.ChainMetrics.SetNewFeedConfigsDetected(float64(len(data.Feeds)))
			monitor.Run(localCtx, data)
		})
	})

	subs.Go(func() {
		m.HTTPServer.Run(rootCtx)
	})

	// Handle signals from the OS
	subs.Go(func() {
		osSignalsCh := make(chan os.Signal, 1)
		signal.Notify(osSignalsCh, syscall.SIGINT, syscall.SIGTERM)
		var sig os.Signal
		select {
		case sig = <-osSignalsCh:
			m.Log.Infow("received signal. Stopping", "signal", sig)
			cancel()
		case <-rootCtx.Done():
		}
	})

	subs.Wait()
}
