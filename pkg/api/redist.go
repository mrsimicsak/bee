// Copyright 2021 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package api

import (
	"net/http"

	"github.com/ethersphere/bee/pkg/jsonhttp"
	"github.com/ethersphere/bee/pkg/tracing"
)

type currentRoundResponse struct {
	CurrentRound uint64 `json:"currentRound"` // ChainTip (block height).
	CurrentBlock uint64 `json:"currentblock"` // The block number of the last postage event.
}

// chainStateHandler returns the current chain state.
func (s *Service) currentRoundHandler(w http.ResponseWriter, r *http.Request) {
	logger := tracing.NewLoggerWithTraceID(r.Context(), s.logger.WithName("get_chainstate").Build())

	chainTip, err := s.chainBackend.BlockNumber(r.Context())
	if err != nil {
		logger.Debug("get block number failed", "error", err)
		logger.Error(nil, "get block number failed")
		jsonhttp.InternalServerError(w, "block number unavailable")
		return
	}
	jsonhttp.OK(w, currentRoundResponse{
		CurrentBlock: chainTip,
	})
}
