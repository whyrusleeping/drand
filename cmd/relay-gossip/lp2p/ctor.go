package lp2p

import (
	"context"
	"crypto/rand"
	"fmt"
	"io/ioutil"
	mrand "math/rand"
	"os"
	"time"

	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/namespace"
	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/crypto"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-peerstore/pstoreds"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pubsubpb "github.com/libp2p/go-libp2p-pubsub/pb"
	libp2ptls "github.com/libp2p/go-libp2p-tls"
	ma "github.com/multiformats/go-multiaddr"
	"golang.org/x/crypto/blake2b"
	xerrors "golang.org/x/xerrors"
)

var (
	log = logging.Logger("lp2p")
)

func PubSubTopic(nn string) string {
	return fmt.Sprintf("/drand/pubsub/v0.0.0/%s", nn)
}

func ConstructHost(ds datastore.Datastore, priv crypto.PrivKey, listenAddr string,
	bootstrap []ma.Multiaddr) (host.Host, *pubsub.PubSub, error) {
	ctx := context.Background()

	pstoreDs := namespace.Wrap(ds, datastore.NewKey("/peerstore"))
	pstore, err := pstoreds.NewPeerstore(ctx, pstoreDs, pstoreds.DefaultOpts())
	if err != nil {
		return nil, nil, xerrors.Errorf("creating peerstore: %w", err)
	}
	peerId, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		return nil, nil, xerrors.Errorf("computing peerid: %w", err)
	}
	err = pstore.AddPrivKey(peerId, priv)
	if err != nil {
		return nil, nil, xerrors.Errorf("adding priv to keystore: %w", err)
	}

	addrInfos, err := peer.AddrInfosFromP2pAddrs(bootstrap...)
	if err != nil {
		fmt.Printf("%+v", bootstrap)
		return nil, nil, xerrors.Errorf("parsing addrInfos: %+v", err)
	}

	h, err := libp2p.New(ctx,
		libp2p.ListenAddrStrings(listenAddr),
		libp2p.Identity(priv),
		libp2p.Security(libp2ptls.ID, libp2ptls.New),
		libp2p.DisableRelay(),
		libp2p.Peerstore(pstore),
	)
	if err != nil {
		return nil, nil, xerrors.Errorf("constructing host: %w", err)
	}

	p, err := pubsub.NewGossipSub(ctx, h,
		pubsub.WithPeerExchange(true),
		pubsub.WithMessageIdFn(func(pmsg *pubsubpb.Message) string {
			hash := blake2b.Sum256(pmsg.Data)
			return string(hash[:])
		}),
		pubsub.WithDirectPeers(addrInfos),
		pubsub.WithFloodPublish(true),
	)
	if err != nil {
		return nil, nil, xerrors.Errorf("constructing pubsub: %d", err)
	}

	go func() {
		mrand.Shuffle(len(addrInfos), func(i, j int) {
			addrInfos[i], addrInfos[j] = addrInfos[j], addrInfos[i]
		})
		for _, ai := range addrInfos {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := h.Connect(ctx, ai)
			cancel()
			if err != nil {
				log.Warnf("could not bootstrap with: %s", ai)
			}
		}
	}()
	return h, p, nil
}

func LoadOrCreatePrivKey(identityPath string) (crypto.PrivKey, error) {
	privBytes, err := ioutil.ReadFile(identityPath)

	var priv crypto.PrivKey
	switch {
	case err == nil:
		priv, err = crypto.UnmarshalEd25519PrivateKey(privBytes)

		if err != nil {
			return nil, xerrors.Errorf("decoding ed25519 key: %w", err)
		}
		log.Infof("loaded private key")

	case xerrors.Is(err, os.ErrNotExist):
		priv, _, err = crypto.GenerateEd25519Key(rand.Reader)
		if err != nil {
			return nil, xerrors.Errorf("generating private key: %w", err)
		}
		b, err := priv.Raw()
		if err != nil {
			return nil, xerrors.Errorf("marshaling private key: %w", err)
		}
		err = ioutil.WriteFile(identityPath, b, 0600)
		if err != nil {
			return nil, xerrors.Errorf("writing identity file: %w", err)
		}

	default:
		return nil, xerrors.Errorf("getting private key: %w", err)
	}

	return priv, nil
}
