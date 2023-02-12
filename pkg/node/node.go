// Copyright 2020 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package node defines the concept of a Bee node
// by bootstrapping and injecting all necessary
// dependencies.
package node

import (
	"context"
	"errors"
	"fmt"
	"io"
	stdlog "log"
	"net"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/ethersphere/bee/pkg/api"
	"github.com/ethersphere/bee/pkg/auth"
	"github.com/ethersphere/bee/pkg/log"
	"github.com/ethersphere/bee/pkg/metrics"
	"github.com/ethersphere/bee/pkg/p2p"
	"github.com/ethersphere/bee/pkg/postage"
	"github.com/ethersphere/bee/pkg/resolver/multiresolver"
	"github.com/ethersphere/bee/pkg/settlement/swap/erc20"
	"github.com/ethersphere/bee/pkg/storageincentives"
	"github.com/ethersphere/bee/pkg/swarm"
	"github.com/ethersphere/bee/pkg/topology"
	"github.com/ethersphere/bee/pkg/tracing"
	"github.com/ethersphere/bee/pkg/transaction"
	"github.com/ethersphere/bee/pkg/util"
	"github.com/ethersphere/bee/pkg/util/ioutil"
	"github.com/hashicorp/go-multierror"
	"golang.org/x/sync/errgroup"
)

// LoggerName is the tree path name of the logger for this package.
const LoggerName = "node"

type Bee struct {
	p2pService               io.Closer
	p2pHalter                p2p.Halter
	ctxCancel                context.CancelFunc
	apiCloser                io.Closer
	apiServer                *http.Server
	debugAPIServer           *http.Server
	resolverCloser           io.Closer
	errorLogWriter           io.Writer
	tracerCloser             io.Closer
	tagsCloser               io.Closer
	stateStoreCloser         io.Closer
	localstoreCloser         io.Closer
	nsCloser                 io.Closer
	topologyCloser           io.Closer
	topologyHalter           topology.Halter
	pusherCloser             io.Closer
	pullerCloser             io.Closer
	accountingCloser         io.Closer
	pullSyncCloser           io.Closer
	pssCloser                io.Closer
	ethClientCloser          func()
	transactionMonitorCloser io.Closer
	transactionCloser        io.Closer
	listenerCloser           io.Closer
	postageServiceCloser     io.Closer
	priceOracleCloser        io.Closer
	hiveCloser               io.Closer
	chainSyncerCloser        io.Closer
	depthMonitorCloser       io.Closer
	storageIncetivesCloser   io.Closer
	shutdownInProgress       bool
	shutdownMutex            sync.Mutex
	syncingStopped           *util.Signaler
}

type Options struct {
	DataDir                       string
	CacheCapacity                 uint64
	DBOpenFilesLimit              uint64
	DBWriteBufferSize             uint64
	DBBlockCacheCapacity          uint64
	DBDisableSeeksCompaction      bool
	APIAddr                       string
	DebugAPIAddr                  string
	Addr                          string
	NATAddr                       string
	EnableWS                      bool
	WelcomeMessage                string
	Bootnodes                     []string
	CORSAllowedOrigins            []string
	Logger                        log.Logger
	TracingEnabled                bool
	TracingEndpoint               string
	TracingServiceName            string
	PaymentThreshold              string
	PaymentTolerance              int64
	PaymentEarly                  int64
	ResolverConnectionCfgs        []multiresolver.ConnectionConfig
	RetrievalCaching              bool
	BootnodeMode                  bool
	BlockchainRpcEndpoint         string
	SwapFactoryAddress            string
	SwapLegacyFactoryAddresses    []string
	SwapInitialDeposit            string
	SwapEnable                    bool
	ChequebookEnable              bool
	FullNodeMode                  bool
	Transaction                   string
	BlockHash                     string
	PostageContractAddress        string
	PostageContractStartBlock     uint64
	StakingContractAddress        string
	PriceOracleAddress            string
	RedistributionContractAddress string
	BlockTime                     time.Duration
	DeployGasPrice                string
	WarmupTime                    time.Duration
	ChainID                       int64
	Resync                        bool
	BlockProfile                  bool
	MutexProfile                  bool
	StaticNodes                   []swarm.Address
	AllowPrivateCIDRs             bool
	Restricted                    bool
	TokenEncryptionKey            string
	AdminPasswordHash             string
	UsePostageSnapshot            bool
	EnableStorageIncentives       bool
}

const (
	refreshRate                   = int64(4500000)            // accounting units refreshed per second
	lightFactor                   = 10                        // downscale payment thresholds and their change rate, and refresh rates by this for light nodes
	lightRefreshRate              = refreshRate / lightFactor // refresh rate used by / for light nodes
	basePrice                     = 10000                     // minimal price for retrieval and pushsync requests of maximum proximity
	postageSyncingStallingTimeout = 10 * time.Minute          //
	postageSyncingBackoffTimeout  = 5 * time.Second           //
	minPaymentThreshold           = 2 * refreshRate           // minimal accepted payment threshold of full nodes
	maxPaymentThreshold           = 24 * refreshRate          // maximal accepted payment threshold of full nodes
	mainnetNetworkID              = uint64(1)                 //
)

func NewBee(ctx context.Context, networkID uint64, logger log.Logger, o *Options) (b *Bee, err error) {
	tracer, tracerCloser, err := tracing.NewTracer(&tracing.Options{
		Enabled:     o.TracingEnabled,
		Endpoint:    o.TracingEndpoint,
		ServiceName: o.TracingServiceName,
	})
	if err != nil {
		return nil, fmt.Errorf("tracer: %w", err)
	}

	ctx, ctxCancel := context.WithCancel(ctx)
	defer func() {
		// if there's been an error on this function
		// we'd like to cancel the p2p context so that
		// incoming connections will not be possible
		if err != nil {
			ctxCancel()
		}
	}()

	sink := ioutil.WriterFunc(func(p []byte) (int, error) {
		logger.Error(nil, string(p))
		return len(p), nil
	})

	b = &Bee{
		ctxCancel:      ctxCancel,
		errorLogWriter: sink,
		tracerCloser:   tracerCloser,
		syncingStopped: util.NewSignaler(),
	}

	defer func(b *Bee) {
		if err != nil {
			logger.Error(err, "got error, shutting down...")
			if err2 := b.Shutdown(); err2 != nil {
				logger.Error(err2, "got error while shutting down")
			}
		}
	}(b)

	var (
		chainBackend transaction.Backend
		// overlayEthAddress  common.Address
		chainID            int64
		transactionService transaction.Service
		transactionMonitor transaction.Monitor
		// chequebookFactory  chequebook.Factory
		// chequebookService  chequebook.Service = new(noOpChequebookService)
		// chequeStore        chequebook.ChequeStore
		// cashoutService     chequebook.CashoutService
		erc20Service erc20.Service
	)

	chainEnabled := isChainEnabled(o, o.BlockchainRpcEndpoint, logger)

	// var batchStore postage.Storer = new(postage.NoOpBatchStore)
	// var evictFn func([]byte) error

	// if chainEnabled {
	// 	batchStore, err = batchstore.New(
	// 		stateStore,
	// 		func(id []byte) error {
	// 			return evictFn(id)
	// 		},
	// 		logger,
	// 	)
	// 	if err != nil {
	// 		return nil, fmt.Errorf("batchstore: %w", err)
	// 	}
	// }

	chainBackend, chainID, err = InitChain(
		ctx,
		logger,
		o.BlockchainRpcEndpoint,
		o.ChainID,
		o.BlockTime,
		chainEnabled)
	if err != nil {
		return nil, fmt.Errorf("init chain: %w", err)
	}
	b.ethClientCloser = chainBackend.Close

	logger.Info("using chain with network network", "chain_id", chainID, "network_id", networkID)

	if o.ChainID != -1 && o.ChainID != chainID {
		return nil, fmt.Errorf("connected to wrong ethereum network; network chainID %d; configured chainID %d", chainID, o.ChainID)
	}

	b.transactionCloser = tracerCloser
	b.transactionMonitorCloser = transactionMonitor

	var authenticator auth.Authenticator

	if o.Restricted {
		if authenticator, err = auth.New(o.TokenEncryptionKey, o.AdminPasswordHash, logger); err != nil {
			return nil, fmt.Errorf("authenticator: %w", err)
		}
		logger.Info("starting with restricted APIs")
	}

	var debugService *api.Service

	if o.DebugAPIAddr != "" {
		if o.MutexProfile {
			_ = runtime.SetMutexProfileFraction(1)
		}

		if o.BlockProfile {
			runtime.SetBlockProfileRate(1)
		}

		debugAPIListener, err := net.Listen("tcp", o.DebugAPIAddr)
		if err != nil {
			return nil, fmt.Errorf("debug api listener: %w", err)
		}

		debugService = api.New(logger, transactionService, chainBackend, o.CORSAllowedOrigins)
		debugService.MountTechnicalDebug()
		// debugService.SetProbe(probe)

		debugAPIServer := &http.Server{
			IdleTimeout:       30 * time.Second,
			ReadHeaderTimeout: 3 * time.Second,
			Handler:           debugService,
			ErrorLog:          stdlog.New(b.errorLogWriter, "", 0),
		}

		go func() {
			logger.Info("starting debug server", "address", debugAPIListener.Addr())

			if err := debugAPIServer.Serve(debugAPIListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Debug("debug api server failed to start", "error", err)
				logger.Error(nil, "debug api server failed to start")
			}
		}()

		b.debugAPIServer = debugAPIServer
	}

	// var apiService *api.Service

	// if o.Restricted {
	// 	apiService = api.New(*publicKey, pssPrivateKey.PublicKey, overlayEthAddress, logger, transactionService, batchStore, beeNodeMode, o.ChequebookEnable, o.SwapEnable, chainBackend, o.CORSAllowedOrigins)
	// 	apiService.MountTechnicalDebug()
	// 	apiService.SetProbe(probe)

	// 	apiServer := &http.Server{
	// 		IdleTimeout:       30 * time.Second,
	// 		ReadHeaderTimeout: 3 * time.Second,
	// 		Handler:           apiService,
	// 		ErrorLog:          stdlog.New(b.errorLogWriter, "", 0),
	// 	}

	// 	apiListener, err := net.Listen("tcp", o.APIAddr)
	// 	if err != nil {
	// 		return nil, fmt.Errorf("api listener: %w", err)
	// 	}

	// 	go func() {
	// 		logger.Info("starting debug & api server", "address", apiListener.Addr())

	// 		if err := apiServer.Serve(apiListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
	// 			logger.Debug("debug & api server failed to start", "error", err)
	// 			logger.Error(nil, "debug & api server failed to start")
	// 		}
	// 	}()

	// 	b.apiServer = apiServer
	// 	b.apiCloser = apiServer
	// }

	// // Sync the with the given Ethereum backend:
	// isSynced, _, err := transaction.IsSynced(ctx, chainBackend, maxDelay)
	// if err != nil {
	// 	return nil, fmt.Errorf("is synced: %w", err)
	// }
	// if !isSynced {
	// 	logger.Info("waiting to sync with the Ethereum backend")

	// 	err := transaction.WaitSynced(ctx, logger, chainBackend, maxDelay)
	// 	if err != nil {
	// 		return nil, fmt.Errorf("waiting backend sync: %w", err)
	// 	}
	// }

	// if o.SwapEnable {
	// 	chequebookFactory, err = InitChequebookFactory(
	// 		logger,
	// 		chainBackend,
	// 		chainID,
	// 		transactionService,
	// 		o.SwapFactoryAddress,
	// 		o.SwapLegacyFactoryAddresses,
	// 	)
	// 	if err != nil {
	// 		return nil, err
	// 	}

	// 	if err = chequebookFactory.VerifyBytecode(ctx); err != nil {
	// 		return nil, fmt.Errorf("factory fail: %w", err)
	// 	}

	// 	erc20Address, err := chequebookFactory.ERC20Address(ctx)
	// 	if err != nil {
	// 		return nil, fmt.Errorf("factory fail: %w", err)
	// 	}

	// 	erc20Service = erc20.New(transactionService, erc20Address)

	// 	if o.ChequebookEnable && chainEnabled {
	// 		chequebookService, err = InitChequebookService(
	// 			ctx,
	// 			logger,
	// 			stateStore,
	// 			signer,
	// 			chainID,
	// 			chainBackend,
	// 			overlayEthAddress,
	// 			transactionService,
	// 			chequebookFactory,
	// 			o.SwapInitialDeposit,
	// 			o.DeployGasPrice,
	// 			erc20Service,
	// 		)
	// 		if err != nil {
	// 			return nil, err
	// 		}
	// 	}

	// 	chequeStore, cashoutService = initChequeStoreCashout(
	// 		stateStore,
	// 		chainBackend,
	// 		chequebookFactory,
	// 		chainID,
	// 		overlayEthAddress,
	// 		transactionService,
	// 	)
	// }

	// pubKey, _ := signer.PublicKey()
	// if err != nil {
	// 	return nil, err
	// }

	// // if theres a previous transaction hash, and not a new chequebook deployment on a node starting from scratch
	// // get old overlay
	// // mine nonce that gives similar new overlay
	// nonce, nonceExists, err := overlayNonceExists(stateStore)
	// if err != nil {
	// 	return nil, fmt.Errorf("check presence of nonce: %w", err)
	// }

	// swarmAddress, err := crypto.NewOverlayAddress(*pubKey, networkID, nonce)
	// if err != nil {
	// 	return nil, fmt.Errorf("compute overlay address: %w", err)
	// }
	// logger.Info("using overlay address", "address", swarmAddress)

	// if !nonceExists {
	// 	err := setOverlayNonce(stateStore, nonce)
	// 	if err != nil {
	// 		return nil, fmt.Errorf("statestore: save new overlay nonce: %w", err)
	// 	}

	// 	err = SetOverlayInStore(swarmAddress, stateStore)
	// 	if err != nil {
	// 		return nil, fmt.Errorf("statestore: save new overlay: %w", err)
	// 	}
	// }

	var (
		eventListener postage.Listener
	)

	// save
	// chainCfg, _ := config.GetByChainID(chainID)

	var (
		agent *storageincentives.Agent
	)

	// redistributionContractAddress := chainCfg.RedistributionAddress
	// if o.RedistributionContractAddress != "" {
	// 	if !common.IsHexAddress(o.RedistributionContractAddress) {
	// 		return nil, errors.New("malformed redistribution contract address")
	// 	}
	// 	redistributionContractAddress = common.HexToAddress(o.RedistributionContractAddress)
	// }
	// redistributionContractABI, err := abi.JSON(strings.NewReader(chainCfg.RedistributionABI))
	// if err != nil {
	// 	return nil, fmt.Errorf("unable to parse redistribution ABI: %w", err)
	// }

	// redistributionContract := redistribution.New(swarmAddress, logger, transactionService, redistributionContractAddress, redistributionContractABI)
	agent = storageincentives.New(chainBackend, logger, o.BlockTime, storageincentives.DefaultBlocksPerRound, storageincentives.DefaultBlocksPerPhase)
	b.storageIncetivesCloser = agent

	if o.DebugAPIAddr != "" {

		if agent != nil {
			debugService.MustRegisterMetrics(agent.Metrics()...)
		}

		if eventListener != nil {
			if ls, ok := eventListener.(metrics.Collector); ok {
				debugService.MustRegisterMetrics(ls.Metrics()...)
			}
		}

		if l, ok := logger.(metrics.Collector); ok {
			debugService.MustRegisterMetrics(l.Metrics()...)
		}
		// if nsMetrics, ok := ns.(metrics.Collector); ok {
		// 	debugService.MustRegisterMetrics(nsMetrics.Metrics()...)
		// }
		// debugService.MustRegisterMetrics(pseudosettleService.Metrics()...)
		// if swapService != nil {
		// 	debugService.MustRegisterMetrics(swapService.Metrics()...)
		// }

		extraOpts := api.ExtraOptions{
			// Pingpong:         pingPong,
			// TopologyDriver:   kad,
			// LightNodes:       lightNodes,
			// Accounting:       acc,
			// Pseudosettle:     pseudosettleService,
			// Swap:             swapService,
			// Chequebook:       chequebookService,
			BlockTime: o.BlockTime,
			// Tags:             tagService,
			// Storer:           ns,
			// Resolver:         multiResolver,
			// Pss:              pssService,
			// TraversalService: traversalService,
			// Pinning:          pinningService,
			// FeedFactory:      feedFactory,
			// Post:             post,
			// PostageContract:  postageStampContractService,
			// Staking:          stakingContract,
			// Steward:          steward,
			// SyncStatus:       syncStatusFn,
			// IndexDebugger:    storer,
		}

		debugService.Configure(authenticator, tracer, api.Options{
			CORSAllowedOrigins: o.CORSAllowedOrigins,
			WsPingPeriod:       60 * time.Second,
			Restricted:         o.Restricted,
		}, extraOpts, chainID, erc20Service)

		debugService.MountTechnicalDebug()
	}

	// if err := kad.Start(ctx); err != nil {
	// 	return nil, err
	// }

	// if err := p2ps.Ready(); err != nil {
	// 	return nil, err
	// }

	return b, nil
}

func (b *Bee) SyncingStopped() chan struct{} {
	return b.syncingStopped.C
}

func (b *Bee) Shutdown() error {
	var mErr error

	// if a shutdown is already in process, return here
	b.shutdownMutex.Lock()
	if b.shutdownInProgress {
		b.shutdownMutex.Unlock()
		return ErrShutdownInProgress
	}
	b.shutdownInProgress = true
	b.shutdownMutex.Unlock()

	// halt kademlia while shutting down other
	// components.
	if b.topologyHalter != nil {
		b.topologyHalter.Halt()
	}

	// halt p2p layer from accepting new connections
	// while shutting down other components
	if b.p2pHalter != nil {
		b.p2pHalter.Halt()
	}
	// tryClose is a convenient closure which decrease
	// repetitive io.Closer tryClose procedure.
	tryClose := func(c io.Closer, errMsg string) {
		if c == nil {
			return
		}
		if err := c.Close(); err != nil {
			mErr = multierror.Append(mErr, fmt.Errorf("%s: %w", errMsg, err))
		}
	}

	tryClose(b.apiCloser, "api")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var eg errgroup.Group
	if b.apiServer != nil {
		eg.Go(func() error {
			if err := b.apiServer.Shutdown(ctx); err != nil {
				return fmt.Errorf("api server: %w", err)
			}
			return nil
		})
	}
	if b.debugAPIServer != nil {
		eg.Go(func() error {
			if err := b.debugAPIServer.Shutdown(ctx); err != nil {
				return fmt.Errorf("debug api server: %w", err)
			}
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		mErr = multierror.Append(mErr, err)
	}

	var wg sync.WaitGroup
	wg.Add(7)
	go func() {
		defer wg.Done()
		tryClose(b.chainSyncerCloser, "chain syncer")
	}()
	go func() {
		defer wg.Done()
		tryClose(b.pssCloser, "pss")
	}()
	go func() {
		defer wg.Done()
		tryClose(b.pusherCloser, "pusher")
	}()
	go func() {
		defer wg.Done()
		tryClose(b.pullerCloser, "puller")
	}()
	go func() {
		defer wg.Done()
		tryClose(b.accountingCloser, "accounting")
	}()

	b.ctxCancel()
	go func() {
		defer wg.Done()
		tryClose(b.pullSyncCloser, "pull sync")
	}()
	go func() {
		defer wg.Done()
		tryClose(b.hiveCloser, "hive")
	}()

	wg.Wait()

	tryClose(b.p2pService, "p2p server")
	tryClose(b.priceOracleCloser, "price oracle service")

	wg.Add(3)
	go func() {
		defer wg.Done()
		tryClose(b.transactionMonitorCloser, "transaction monitor")
		tryClose(b.transactionCloser, "transaction")
	}()
	go func() {
		defer wg.Done()
		tryClose(b.listenerCloser, "listener")
	}()
	go func() {
		defer wg.Done()
		tryClose(b.postageServiceCloser, "postage service")
	}()

	wg.Wait()

	if c := b.ethClientCloser; c != nil {
		c()
	}

	tryClose(b.tracerCloser, "tracer")
	tryClose(b.tagsCloser, "tag persistence")
	tryClose(b.topologyCloser, "topology driver")
	tryClose(b.nsCloser, "netstore")
	tryClose(b.depthMonitorCloser, "depthmonitor service")
	tryClose(b.storageIncetivesCloser, "storage incentives agent")
	tryClose(b.stateStoreCloser, "statestore")
	tryClose(b.localstoreCloser, "localstore")
	tryClose(b.resolverCloser, "resolver service")

	return mErr
}

var ErrShutdownInProgress error = errors.New("shutdown in progress")

func isChainEnabled(o *Options, swapEndpoint string, logger log.Logger) bool {
	chainDisabled := swapEndpoint == ""
	lightMode := !o.FullNodeMode

	if lightMode && chainDisabled { // ultra light mode is LightNode mode with chain disabled
		logger.Info("starting with a disabled chain backend")
		return false
	}

	logger.Info("starting with an enabled chain backend")
	return true // all other modes operate require chain enabled
}
