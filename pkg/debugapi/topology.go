// Copyright 2020 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package debugapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/ethersphere/bee/pkg/jsonhttp"
	"github.com/ethersphere/bee/pkg/swarm"
)

type binInfo struct {
	BinPopulation     uint     `json:"population"`
	BinConnected      uint     `json:"connected"`
	DisconnectedPeers []string `json:"disconnectedPeers"`
	ConnectedPeers    []string `json:"connectedPeers"`
}

type kadBins struct {
	Bin0  binInfo `json:"bin_0"`
	Bin1  binInfo `json:"bin_1"`
	Bin2  binInfo `json:"bin_2"`
	Bin3  binInfo `json:"bin_3"`
	Bin4  binInfo `json:"bin_4"`
	Bin5  binInfo `json:"bin_5"`
	Bin6  binInfo `json:"bin_6"`
	Bin7  binInfo `json:"bin_7"`
	Bin8  binInfo `json:"bin_8"`
	Bin9  binInfo `json:"bin_9"`
	Bin10 binInfo `json:"bin_10"`
	Bin11 binInfo `json:"bin_11"`
	Bin12 binInfo `json:"bin_12"`
	Bin13 binInfo `json:"bin_13"`
	Bin14 binInfo `json:"bin_14"`
	Bin15 binInfo `json:"bin_15"`
}

type kadTopology struct {
	Base           string    `json:"baseAddr"`       // base address string
	Population     int       `json:"population"`     // known
	Connected      int       `json:"connected"`      // connected count
	Timestamp      time.Time `json:"timestamp"`      // now
	NNLowWatermark int       `json:"nnLowWatermark"` // low watermark for depth calculation
	Depth          uint8     `json:"depth"`          // current depth
	Bins           kadBins   `json:"bins"`           // individual bin info
	LightNodes     binInfo   `json:"lightNodes"`
}

func (k *kadTopology) findBin(po uint8) (bin *binInfo) {
	switch po {
	case 0:
		bin = &k.Bins.Bin0
	case 1:
		bin = &k.Bins.Bin1
	case 2:
		bin = &k.Bins.Bin2
	case 3:
		bin = &k.Bins.Bin3
	case 4:
		bin = &k.Bins.Bin4
	case 5:
		bin = &k.Bins.Bin5
	case 6:
		bin = &k.Bins.Bin6
	case 7:
		bin = &k.Bins.Bin7
	case 8:
		bin = &k.Bins.Bin8
	case 9:
		bin = &k.Bins.Bin9
	case 10:
		bin = &k.Bins.Bin10
	case 11:
		bin = &k.Bins.Bin11
	case 12:
		bin = &k.Bins.Bin12
	case 13:
		bin = &k.Bins.Bin13
	case 14:
		bin = &k.Bins.Bin14
	case 15:
		bin = &k.Bins.Bin15
	}
	return
}

func (k *kadTopology) WithConnectedBin(addr swarm.Address, po uint8) {
	bin := k.findBin(po)
	bin.BinConnected++
	bin.ConnectedPeers = append(bin.ConnectedPeers, addr.String())
}

func (k *kadTopology) WithDisonnectedBin(addr swarm.Address, po uint8) {
	bin := k.findBin(po)
	bin.BinPopulation++
	for _, v := range bin.ConnectedPeers {
		// peer already connected, don't show in the known peers list
		if v == addr.String() {
			return
		}
	}
	bin.DisconnectedPeers = append(bin.DisconnectedPeers, addr.String())
}

func (k *kadTopology) WithBase(base string) {
	k.Base = base
}
func (k *kadTopology) WithPopulation(pop int) {
	k.Population = pop
}
func (k *kadTopology) WithConnected(con int) {
	k.Connected = con
}
func (k *kadTopology) WithNNLowWatermark(wm int) {
	k.NNLowWatermark = wm
}
func (k *kadTopology) WithDepth(d uint8) {
	k.Depth = d
}
func (k *kadTopology) WithLightNodes(connected, poulation uint, connectedPeers, disconnectedPeers []string) {
	k.LightNodes = binInfo{
		BinPopulation:     poulation,
		BinConnected:      connected,
		ConnectedPeers:    connectedPeers,
		DisconnectedPeers: disconnectedPeers,
	}
}

func (s *Service) topologyHandler(w http.ResponseWriter, r *http.Request) {
	params := s.topologyDriver.Snapshot()

	b, err := json.Marshal(params)
	if err != nil {
		s.logger.Errorf("topology marshal to json: %v", err)
		jsonhttp.InternalServerError(w, err)
		return
	}
	w.Header().Set("Content-Type", jsonhttp.DefaultContentTypeHeader)
	_, _ = io.Copy(w, bytes.NewBuffer(b))
}
