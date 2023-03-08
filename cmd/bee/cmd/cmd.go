// Copyright 2020 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ethersphere/bee/pkg/log"
	"github.com/ethersphere/bee/pkg/node"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	optionNameDataDir                    = "data-dir"
	optionNameCacheCapacity              = "cache-capacity"
	optionNameDBOpenFilesLimit           = "db-open-files-limit"
	optionNameDBBlockCacheCapacity       = "db-block-cache-capacity"
	optionNameDBWriteBufferSize          = "db-write-buffer-size"
	optionNameDBDisableSeeksCompaction   = "db-disable-seeks-compaction"
	optionNamePassword                   = "password"
	optionNamePasswordFile               = "password-file"
	optionNameAPIAddr                    = "api-addr"
	optionNameP2PAddr                    = "p2p-addr"
	optionNameNATAddr                    = "nat-addr"
	optionNameP2PWSEnable                = "p2p-ws-enable"
	optionNameDebugAPIEnable             = "debug-api-enable"
	optionNameDebugAPIAddr               = "debug-api-addr"
	optionNameBootnodes                  = "bootnode"
	optionNameNetworkID                  = "network-id"
	optionWelcomeMessage                 = "welcome-message"
	optionCORSAllowedOrigins             = "cors-allowed-origins"
	optionNameTracingEnabled             = "tracing-enable"
	optionNameTracingEndpoint            = "tracing-endpoint"
	optionNameTracingHost                = "tracing-host"
	optionNameTracingPort                = "tracing-port"
	optionNameTracingServiceName         = "tracing-service-name"
	optionNameVerbosity                  = "verbosity"
	optionNamePaymentThreshold           = "payment-threshold"
	optionNamePaymentTolerance           = "payment-tolerance-percent"
	optionNamePaymentEarly               = "payment-early-percent"
	optionNameResolverEndpoints          = "resolver-options"
	optionNameBootnodeMode               = "bootnode-mode"
	optionNameClefSignerEnable           = "clef-signer-enable"
	optionNameClefSignerEndpoint         = "clef-signer-endpoint"
	optionNameClefSignerEthereumAddress  = "clef-signer-ethereum-address"
	optionNameSwapEndpoint               = "swap-endpoint" // deprecated: use rpc endpoint instead
	optionNameBlockchainRpcEndpoint      = "blockchain-rpc-endpoint"
	optionNameSwapFactoryAddress         = "swap-factory-address"
	optionNameSwapLegacyFactoryAddresses = "swap-legacy-factory-addresses"
	optionNameSwapInitialDeposit         = "swap-initial-deposit"
	optionNameSwapEnable                 = "swap-enable"
	optionNameChequebookEnable           = "chequebook-enable"
	optionNameTransactionHash            = "transaction"
	optionNameBlockHash                  = "block-hash"
	optionNameSwapDeploymentGasPrice     = "swap-deployment-gas-price"
	optionNameFullNode                   = "full-node"
	optionNamePostageContractAddress     = "postage-stamp-address"
	optionNamePostageContractStartBlock  = "postage-stamp-start-block"
	optionNamePriceOracleAddress         = "price-oracle-address"
	optionNameRedistributionAddress      = "redistribution-address"
	optionNameStakingAddress             = "staking-address"
	optionNameBlockTime                  = "block-time"
	optionWarmUpTime                     = "warmup-time"
	optionNameMainNet                    = "mainnet"
	optionNameRetrievalCaching           = "cache-retrieval"
	optionNameDevReserveCapacity         = "dev-reserve-capacity"
	optionNameResync                     = "resync"
	optionNamePProfBlock                 = "pprof-profile"
	optionNamePProfMutex                 = "pprof-mutex"
	optionNameStaticNodes                = "static-nodes"
	optionNameAllowPrivateCIDRs          = "allow-private-cidrs"
	optionNameSleepAfter                 = "sleep-after"
	optionNameRestrictedAPI              = "restricted"
	optionNameTokenEncryptionKey         = "token-encryption-key"
	optionNameAdminPasswordHash          = "admin-password"
	optionNameUsePostageSnapshot         = "use-postage-snapshot"
	optionNameStorageIncentivesEnable    = "storage-incentives-enable"
)

// nolint:gochecknoinits
func init() {
	cobra.EnableCommandSorting = false
}

type command struct {
	root             *cobra.Command
	config           *viper.Viper
	passwordReader   passwordReader
	cfgFile          string
	homeDir          string
	isWindowsService bool
}

type option func(*command)

func newCommand(opts ...option) (c *command, err error) {
	c = &command{
		root: &cobra.Command{
			Use:           "bee",
			Short:         "Ethereum Swarm Bee",
			SilenceErrors: true,
			SilenceUsage:  true,
			PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
				return c.initConfig()
			},
		},
	}

	for _, o := range opts {
		o(c)
	}
	if c.passwordReader == nil {
		c.passwordReader = new(stdInPasswordReader)
	}

	// Find home directory.
	if err := c.setHomeDir(); err != nil {
		return nil, err
	}

	c.initGlobalFlags()

	if err := c.initCommandVariables(); err != nil {
		return nil, err
	}

	if err := c.initStartCmd(); err != nil {
		return nil, err
	}

	if err := c.initHasherCmd(); err != nil {
		return nil, err
	}

	if err := c.initInitCmd(); err != nil {
		return nil, err
	}

	c.initVersionCmd()
	c.initDBCmd()

	if err := c.initConfigurateOptionsCmd(); err != nil {
		return nil, err
	}

	return c, nil
}

func (c *command) Execute() (err error) {
	return c.root.Execute()
}

// Execute parses command line arguments and runs appropriate functions.
func Execute() (err error) {
	c, err := newCommand()
	if err != nil {
		return err
	}
	return c.Execute()
}

func (c *command) initGlobalFlags() {
	globalFlags := c.root.PersistentFlags()
	globalFlags.StringVar(&c.cfgFile, "config", "", "config file (default is $HOME/.bee.yaml)")
}

func (c *command) initCommandVariables() error {
	isWindowsService, err := isWindowsService()
	if err != nil {
		return fmt.Errorf("failed to determine if we are running in service: %w", err)
	}

	c.isWindowsService = isWindowsService

	return nil
}

func (c *command) initConfig() (err error) {
	config := viper.New()
	configName := ".bee"
	if c.cfgFile != "" {
		// Use config file from the flag.
		config.SetConfigFile(c.cfgFile)
	} else {
		// Search config in home directory with name ".bee" (without extension).
		config.AddConfigPath(c.homeDir)
		config.SetConfigName(configName)
	}

	// Environment
	config.SetEnvPrefix("bee")
	config.AutomaticEnv() // read in environment variables that match
	config.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))

	if c.homeDir != "" && c.cfgFile == "" {
		c.cfgFile = filepath.Join(c.homeDir, configName+".yaml")
	}

	// If a config file is found, read it in.
	if err := config.ReadInConfig(); err != nil {
		var e viper.ConfigFileNotFoundError
		if !errors.As(err, &e) {
			return err
		}
	}
	c.config = config
	return nil
}

func (c *command) setHomeDir() (err error) {
	if c.homeDir != "" {
		return
	}
	dir, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	c.homeDir = dir
	return nil
}

func (c *command) setAllFlags(cmd *cobra.Command) {
	cmd.Flags().String(optionNameAPIAddr, ":1633", "HTTP API listen address")
	cmd.Flags().Bool(optionNameDebugAPIEnable, false, "enable debug HTTP API")
	cmd.Flags().String(optionNameDebugAPIAddr, ":1635", "debug HTTP API listen address")
	cmd.Flags().Uint64(optionNameNetworkID, 1, "ID of the Swarm network")
	cmd.Flags().StringSlice(optionCORSAllowedOrigins, []string{}, "origins with CORS headers enabled")
	cmd.Flags().Bool(optionNameTracingEnabled, false, "enable tracing")
	cmd.Flags().String(optionNameTracingEndpoint, "127.0.0.1:6831", "endpoint to send tracing data")
	cmd.Flags().String(optionNameTracingHost, "", "host to send tracing data")
	cmd.Flags().String(optionNameTracingPort, "", "port to send tracing data")
	cmd.Flags().String(optionNameTracingServiceName, "bee", "service name identifier for tracing")
	cmd.Flags().String(optionNameVerbosity, "info", "log verbosity level 0=silent, 1=error, 2=warn, 3=info, 4=debug, 5=trace")
	cmd.Flags().String(optionNameBlockchainRpcEndpoint, "", "rpc blockchain endpoint")
	cmd.Flags().String(optionNamePostageContractAddress, "", "postage stamp contract address")
	cmd.Flags().Uint64(optionNamePostageContractStartBlock, 0, "postage stamp contract start block number")
	cmd.Flags().String(optionNamePriceOracleAddress, "", "price oracle contract address")
	cmd.Flags().String(optionNameRedistributionAddress, "", "redistribution contract address")
	cmd.Flags().String(optionNameStakingAddress, "", "staking contract address")
	cmd.Flags().Uint64(optionNameBlockTime, 15, "chain block time")
}

func newLogger(cmd *cobra.Command, verbosity string) (log.Logger, error) {
	var (
		sink   = cmd.OutOrStdout()
		vLevel = log.VerbosityNone
	)

	switch verbosity {
	case "0", "silent":
		sink = io.Discard
	case "1", "error":
		vLevel = log.VerbosityError
	case "2", "warn":
		vLevel = log.VerbosityWarning
	case "3", "info":
		vLevel = log.VerbosityInfo
	case "4", "debug":
		vLevel = log.VerbosityDebug
	case "5", "trace":
		vLevel = log.VerbosityDebug + 1 // For backwards compatibility, just enable v1 debugging as trace.
	default:
		return nil, fmt.Errorf("unknown verbosity level %q", verbosity)
	}

	log.ModifyDefaults(
		log.WithTimestamp(),
		log.WithLogMetrics(),
	)

	return log.NewLogger(
		node.LoggerName,
		log.WithSink(sink),
		log.WithVerbosity(vLevel),
	).Register(), nil
}
