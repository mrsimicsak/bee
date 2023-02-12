// Copyright 2022 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storageincentives

import (
	"context"
	"errors"
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethersphere/bee/pkg/log"
	"github.com/ethersphere/bee/pkg/postage"
	"github.com/ethersphere/bee/pkg/postage/postagecontract"
	"github.com/ethersphere/bee/pkg/storage"
	"github.com/ethersphere/bee/pkg/storageincentives/redistribution"
	"github.com/ethersphere/bee/pkg/swarm"
)

const loggerName = "storageincentives"

const (
	DefaultBlocksPerRound = 152
	DefaultBlocksPerPhase = DefaultBlocksPerRound / 4
)

type ChainBackend interface {
	BlockNumber(context.Context) (uint64, error)
	HeaderByNumber(context.Context, *big.Int) (*types.Header, error)
}

type Monitor interface {
	IsFullySynced() bool
}

type Agent struct {
	logger         log.Logger
	metrics        metrics
	backend        ChainBackend
	blocksPerRound uint64
	monitor        Monitor
	contract       redistribution.Contract
	batchExpirer   postagecontract.PostageBatchExpirer
	reserve        postage.Storer
	sampler        storage.Sampler
	overlay        swarm.Address
	quit           chan struct{}
	wg             sync.WaitGroup
}

func New(
	backend ChainBackend,
	logger log.Logger,
	blockTime time.Duration, blocksPerRound, blocksPerPhase uint64) *Agent {

	s := &Agent{
		metrics:        newMetrics(),
		backend:        backend,
		logger:         logger.WithName(loggerName).Register(),
		blocksPerRound: blocksPerRound,
		quit:           make(chan struct{}),
	}

	s.wg.Add(1)
	go s.start(blockTime, blocksPerRound, blocksPerPhase)

	return s
}

// start polls the current block number, calculates, and publishes only once the current phase.
// Each round is blocksPerRound long and is divided in to three blocksPerPhase long phases: commit, reveal, claim.
// The sample phase is triggered upon entering the claim phase and may run until the end of the commit phase.
// If our neighborhood is selected to participate, a sample is created during the sample phase. In the commit phase,
// the sample is submitted, and in the reveal phase, the obfuscation key from the commit phase is submitted.
// Next, in the claim phase, we check if we've won, and the cycle repeats. The cycle must occur in the length of one round.
func (a *Agent) start(blockTime time.Duration, blocksPerRound, blocksPerPhase uint64) {

	defer a.wg.Done()

	var (
		mtx         sync.Mutex
		round       uint64
		phaseEvents = newEvents()
	)

	// cancel all possible running phases
	defer phaseEvents.Close()

	commitF := func(ctx context.Context) {
		phaseEvents.Cancel(claim)

	}

	// when the sample finishes, if we are in the commit phase, run commit
	phaseEvents.On(sampleEnd, func(ctx context.Context, previous PhaseType) {
		if previous == commit {
			commitF(ctx)
		}
	})

	// when we enter the commit phase, if the sample is already finished, run commit
	phaseEvents.On(commit, func(ctx context.Context, previous PhaseType) {
		if previous == sampleEnd {
			commitF(ctx)
		}
	})

	phaseEvents.On(reveal, func(ctx context.Context, _ PhaseType) {

		// cancel previous executions of the commit and sample phases
		phaseEvents.Cancel(commit, sample, sampleEnd)

	})

	phaseEvents.On(claim, func(ctx context.Context, _ PhaseType) {

		phaseEvents.Cancel(reveal)

	})

	var (
		prevPhase    PhaseType = -1
		currentPhase PhaseType
		checkEvery   uint64 = 1
	)

	// optimization, we do not need to check the phase change at every new block
	if blocksPerPhase > 10 {
		checkEvery = 5
	}

	for {
		select {
		case <-a.quit:
			return
		case <-time.After(blockTime * time.Duration(checkEvery)):
		}

		a.metrics.BackendCalls.Inc()
		block, err := a.backend.BlockNumber(context.Background())
		if err != nil {
			a.metrics.BackendErrors.Inc()
			a.logger.Error(err, "getting block number")
			continue
		}

		mtx.Lock()
		round = block / blocksPerRound
		a.metrics.Round.Set(float64(round))

		p := block % blocksPerRound
		if p < blocksPerPhase {
			currentPhase = commit // [0, 37]
		} else if p >= blocksPerPhase && p < 2*blocksPerPhase { // [38, 75]
			currentPhase = reveal
		} else if p >= 2*blocksPerPhase {
			currentPhase = claim // [76, 151]
		}

		// write the current phase only once

		if currentPhase != prevPhase {

			a.metrics.CurrentPhase.Set(float64(currentPhase))

			a.logger.Info("entering phase", "phase", currentPhase.String(), "round", round, "block", block)

			phaseEvents.Publish(currentPhase)
			if currentPhase == claim {
				phaseEvents.Publish(sample) // trigger sample along side the claim phase
			}
		}

		prevPhase = currentPhase

		mtx.Unlock()
	}
}

func (a *Agent) getPreviousRoundTime(ctx context.Context) (time.Duration, error) {

	a.metrics.BackendCalls.Inc()
	block, err := a.backend.BlockNumber(ctx)
	if err != nil {
		a.metrics.BackendErrors.Inc()
		return 0, err
	}

	previousRoundBlockNumber := ((block / a.blocksPerRound) - 1) * a.blocksPerRound

	a.metrics.BackendCalls.Inc()
	timeLimiterBlock, err := a.backend.HeaderByNumber(ctx, new(big.Int).SetUint64(previousRoundBlockNumber))
	if err != nil {
		a.metrics.BackendErrors.Inc()
		return 0, err
	}

	return time.Duration(timeLimiterBlock.Time) * time.Second / time.Nanosecond, nil
}

func (a *Agent) Close() error {
	close(a.quit)

	stopped := make(chan struct{})
	go func() {
		a.wg.Wait()
		close(stopped)
	}()

	select {
	case <-stopped:
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("stopping incentives with ongoing worker goroutine")
	}
}
