package lightnode

import (
	"github.com/ethersphere/bee/pkg/p2p"
	"github.com/ethersphere/bee/pkg/swarm"
	"github.com/ethersphere/bee/pkg/topology"
	"github.com/ethersphere/bee/pkg/topology/pslice"
)

type Container struct {
	connectedPeers    *pslice.PSlice
	disconnectedPeers *pslice.PSlice
}

func NewContainer() *Container {
	return &Container{
		connectedPeers:    pslice.New(1),
		disconnectedPeers: pslice.New(1),
	}
}

const defaultBin = uint8(0)

func (c *Container) Connected(peer p2p.Peer) {
	addr := peer.Address
	c.connectedPeers.Add(addr, defaultBin)
	c.disconnectedPeers.Remove(addr, defaultBin)
}

func (c *Container) Disconnected(peer p2p.Peer) {
	addr := peer.Address
	if found := c.connectedPeers.Exists(addr); found {
		c.connectedPeers.Remove(addr, defaultBin)
		c.disconnectedPeers.Add(addr, defaultBin)
	}
}

func (c *Container) BinInfo() topology.BinInfo {
	return topology.BinInfo{
		BinPopulation:     uint(c.connectedPeers.Length()),
		BinConnected:      uint(c.connectedPeers.Length()),
		DisconnectedPeers: toAddrs(c.disconnectedPeers),
		ConnectedPeers:    toAddrs(c.connectedPeers),
	}
}

func toAddrs(s *pslice.PSlice) (addrs []string) {
	s.EachBin(func(addr swarm.Address, po uint8) (bool, bool, error) {
		addrs = append(addrs, addr.String())
		return false, false, nil
	})

	return
}
