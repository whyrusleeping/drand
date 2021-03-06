package client

import (
	"context"
	"sync"

	"github.com/drand/drand/cmd/relay-gossip/lp2p"
	"github.com/drand/drand/protobuf/drand"
	"github.com/gogo/protobuf/proto"
	logging "github.com/ipfs/go-log/v2"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"golang.org/x/xerrors"
)

var (
	log = logging.Logger("drand-client")
)

type Client struct {
	cancel func()
	latest uint64

	subs struct {
		sync.Mutex
		M map[*int]chan drand.PublicRandResponse
	}
}

// NewWtihPubsub creates a gossip randomness client.
func NewWithPubsub(ps *pubsub.PubSub, networkName string) (*Client, error) {
	t, err := ps.Join(lp2p.PubSubTopic(networkName))
	if err != nil {
		return nil, xerrors.Errorf("joining pubsub: %w", err)
	}
	s, err := t.Subscribe()
	if err != nil {
		return nil, xerrors.Errorf("subscribe: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{
		cancel: cancel,
	}
	c.subs.M = make(map[*int]chan drand.PublicRandResponse)

	go func() {
		for {
			msg, err := s.Next(ctx)
			if ctx.Err() != nil {
				c.subs.Lock()
				for _, ch := range c.subs.M {
					close(ch)
				}
				c.subs.M = make(map[*int]chan drand.PublicRandResponse)
				c.subs.Unlock()
				t.Close()
				s.Cancel()
				return
			}
			if err != nil {
				log.Warnf("topic.Next error: %+v", err)
				continue
			}
			var rand drand.PublicRandResponse
			err = proto.Unmarshal(msg.Data, &rand)
			if err != nil {
				log.Warnf("unmarshaling randomness: %+v", err)
				continue
			}

			// TODO: verification, need to pass drand network public key in

			if c.latest >= rand.Round {
				continue
			}
			c.latest = rand.Round

			c.subs.Lock()
			for _, ch := range c.subs.M {
				select {
				case ch <- rand:
				default:
					log.Warn("randomness notification dropped due to a full channel")
				}
			}
			c.subs.Unlock()

		}
	}()

	return c, nil
}

type UnsubFunc func()

// Sub subscribes to notfications about new randomness.
// Client instnace owns the channel after it is passed to Sub function,
// thus the channel should not be closed by library user
//
// It is recommended to use a buffered channel. If the channel is full,
// notification about randomness will be dropped.
//
// Notification channels will be closed when the client is Closed
func (c *Client) Sub(ch chan drand.PublicRandResponse) UnsubFunc {
	id := new(int)
	c.subs.Lock()
	c.subs.M[id] = ch
	c.subs.Unlock()

	return func() {
		c.subs.Lock()
		delete(c.subs.M, id)
		close(ch)
		c.subs.Unlock()
	}
}

// Close stops Client, cancels PubSub subscription and closes the topic.
func (c *Client) Close() error {
	c.cancel()
	return nil
}

// TODO: New for users without libp2p already running
