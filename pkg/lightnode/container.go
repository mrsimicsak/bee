package lightnode

import (
	"github.com/ethersphere/bee/pkg/kademlia/pslice"
	"github.com/ethersphere/bee/pkg/p2p"
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
