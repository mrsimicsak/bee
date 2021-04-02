package announce

import (
	"context"
	"sync"

	"github.com/ethersphere/bee/pkg/discovery"
	"github.com/ethersphere/bee/pkg/logging"
	"github.com/ethersphere/bee/pkg/p2p"
	"github.com/ethersphere/bee/pkg/swarm"
	"github.com/ethersphere/bee/pkg/topology/pslice"
)

type Announcer struct {
	logger    logging.Logger   // logger
	discovery discovery.Driver // the discovery driver
	p2p       p2p.Service      // p2p service to connect to nodes with
	wg        sync.WaitGroup
}

func NewAnnouncer(disc discovery.Driver, p2p p2p.Service, logger logging.Logger) *Announcer {
	return &Announcer{
		logger:    logger,
		discovery: disc,
		p2p:       p2p,
		wg:        sync.WaitGroup{},
	}
}

func (a *Announcer) SetP2P(p p2p.Service) {
	a.p2p = p
}

func (a *Announcer) SetDiscovery(disc discovery.Driver) {
	a.discovery = disc
}

// announce a newly connected peer to our connected peers, but also
// notify the peer about our already connected peers
func (k *Announcer) Announce(ctx context.Context, connectedPeers *pslice.PSlice, peer swarm.Address) error {
	addrs := []swarm.Address{}

	_ = connectedPeers.EachBinRev(func(connectedPeer swarm.Address, _ uint8) (bool, bool, error) {
		if connectedPeer.Equal(peer) {
			return false, false, nil
		}

		addrs = append(addrs, connectedPeer)

		// this needs to be in a separate goroutine since a peer we are gossipping to might
		// be slow and since this function is called with the same context from kademlia connect
		// function, this might result in the unfortunate situation where we end up on
		// `err := k.discovery.BroadcastPeers(ctx, peer, addrs...)` with an already expired context
		// indicating falsely, that the peer connection has timed out.
		k.wg.Add(1)
		go func(connectedPeer swarm.Address) {
			defer k.wg.Done()
			if err := k.discovery.BroadcastPeers(context.Background(), connectedPeer, peer); err != nil {
				k.logger.Debugf("could not gossip peer %s to peer %s: %v", peer, connectedPeer, err)
			}
		}(connectedPeer)

		return false, false, nil
	})

	if len(addrs) == 0 {
		return nil
	}

	err := k.discovery.BroadcastPeers(ctx, peer, addrs...)
	if err != nil {
		_ = k.p2p.Disconnect(peer)
	}

	return err
}
