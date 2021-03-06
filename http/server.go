package http

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/drand/drand/beacon"
	"github.com/drand/drand/key"
	"github.com/drand/drand/log"
	"github.com/drand/drand/protobuf/drand"

	json "github.com/nikkolasg/hexjson"
)

var (
	// Timeout for how long to wait for the drand.PublicClient before timing out
	reqTimeout = 5 * time.Second
)

// New creates an HTTP handler for the public Drand API
func New(ctx context.Context, client drand.PublicClient, logger log.Logger) (http.Handler, error) {
	if logger == nil {
		logger = log.DefaultLogger
	}
	handler := handler{
		timeout:     reqTimeout,
		client:      client,
		groupInfo:   nil,
		log:         logger,
		pending:     make([]chan []byte, 0),
		latestRound: 0,
	}

	go handler.Watch(ctx)

	mux := http.NewServeMux()
	//TODO: aggregated bulk round responses.
	mux.HandleFunc("/public/latest", handler.LatestRand)
	mux.HandleFunc("/public/", handler.PublicRand)
	mux.HandleFunc("/group", handler.Group)
	return mux, nil
}

type handler struct {
	timeout   time.Duration
	client    drand.PublicClient
	groupInfo *key.Group
	log       log.Logger

	// synchronization for blocking writes until randomness available.
	pendingLk   sync.RWMutex
	pending     []chan []byte
	latestRound uint64
}

func (h *handler) Watch(ctx context.Context) {
RESET:
	stream, err := h.client.PublicRandStream(context.Background(), &drand.PublicRandRequest{})
	if err != nil {
		return
	}

	for {
		next, err := stream.Recv()
		if err != nil {
			h.log.Warn("http_server", "random stream round failed", "err", err)
			goto RESET
		}

		bytes, err := json.Marshal(next)

		h.pendingLk.Lock()
		if h.latestRound+1 != next.Round && h.latestRound != 0 {
			// we missed a round, or similar. don't send bad data to peers.
			h.log.Warn("http_server", "unexpected round for watch", "err", fmt.Sprintf("expected %d, saw %d", h.latestRound+1, next.Round))
			bytes = []byte{}
		}
		h.latestRound = next.Round
		pending := h.pending
		h.pending = make([]chan []byte, 0)
		h.pendingLk.Unlock()

		for _, waiter := range pending {
			waiter <- bytes
		}

		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

func (h *handler) group(ctx context.Context) *key.Group {
	if h.groupInfo != nil {
		return h.groupInfo
	}

	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()
	pkt, err := h.client.Group(ctx, &drand.GroupRequest{})
	if err != nil {
		h.log.Warn("msg", "group fetch failed", "err", err)
		return nil
	}
	if pkt == nil {
		h.log.Warn("msg", "group fetch didn't return group info")
		return nil
	}
	parsedPkt, err := key.GroupFromProto(pkt)
	if err != nil {
		h.log.Warn("msg", "invalid group fetch", "err", err)
		return nil
	}
	h.groupInfo = parsedPkt
	return parsedPkt
}

func (h *handler) getRand(ctx context.Context, round uint64) ([]byte, error) {
	// First see if we should get on the synchronized 'wait for next release' bandwagon.
	block := false
	h.pendingLk.RLock()
	block = (h.latestRound+1 == round)
	h.pendingLk.RUnlock()
	// If so, prepare, and if we're still sync'd, add ourselves to the list of waiters.
	if block {
		ch := make(chan []byte)
		h.pendingLk.Lock()
		block = (h.latestRound+1 == round)
		if block {
			h.pending = append(h.pending, ch)
		}
		h.pendingLk.Unlock()
		// If that was successful, we can now block until we're notified.
		if block {
			select {
			case r := <-ch:
				return r, nil
			case <-ctx.Done():
				h.pendingLk.Lock()
				defer h.pendingLk.Unlock()
				for i, c := range h.pending {
					if c == ch {
						h.pending = append(h.pending[:i], h.pending[i+1:]...)
						break
					}
				}
				close(ch)
				return nil, ctx.Err()
			}
		}
	}

	req := drand.PublicRandRequest{Round: round}
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()
	resp, err := h.client.PublicRand(ctx, &req)

	if err != nil {
		return nil, err
	}

	return json.Marshal(resp)
}

func (h *handler) PublicRand(w http.ResponseWriter, r *http.Request) {
	// Get the round.
	round := strings.Replace(r.URL.Path, "/public/", "", 1)
	roundN, err := strconv.ParseUint(round, 10, 64)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		h.log.Warn("http_server", "failed to parse client round", "client", r.RemoteAddr, "req", url.PathEscape(r.URL.Path))
		return
	}

	data, err := h.getRand(r.Context(), roundN)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		h.log.Warn("http_server", "failed to get randomness", "client", r.RemoteAddr, "req", url.PathEscape(r.URL.Path), "err", err)
		return
	}

	grp := h.group(r.Context())
	roundExpectedTime := time.Now()
	if grp != nil {
		roundExpectedTime = time.Unix(beacon.TimeOfRound(grp.Period, grp.GenesisTime, roundN), 0)
	}

	// Headers per recommendation for static assets at
	// https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Cache-Control
	w.Header().Set("Cache-Control", "public, max-age=604800, immutable")
	w.Header().Set("Expires", time.Now().Add(7*24*time.Hour).Format(http.TimeFormat))
	http.ServeContent(w, r, "rand.json", roundExpectedTime, bytes.NewReader(data))
}

func (h *handler) LatestRand(w http.ResponseWriter, r *http.Request) {
	req := drand.PublicRandRequest{Round: 0}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()

	resp, err := h.client.PublicRand(ctx, &req)

	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		h.log.Warn("http_server", "failed to get randomness", "client", r.RemoteAddr, "req", url.PathEscape(r.URL.Path), "err", err)
		return
	}

	data, err := json.Marshal(resp)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		h.log.Warn("http_server", "failed to marshal randomness", "client", r.RemoteAddr, "req", url.PathEscape(r.URL.Path), "err", err)
		return
	}

	grp := h.group(r.Context())
	roundTime := time.Now()
	nextTime := time.Now()
	if grp != nil {
		roundTime = time.Unix(beacon.TimeOfRound(grp.Period, grp.GenesisTime, resp.Round), 0)
		nextTime = time.Unix(beacon.TimeOfRound(grp.Period, grp.GenesisTime, resp.Round+1), 0)
	}

	remaining := nextTime.Sub(time.Now())
	if remaining > 0 && remaining < grp.Period {
		seconds := int(math.Ceil(remaining.Seconds()))
		w.Header().Set("Cache-Control", fmt.Sprintf("max-age:%d, public", seconds))
	} else {
		h.log.Warn("http_server", "latest rand in the past", "client", r.RemoteAddr, "req", url.PathEscape(r.URL.Path), "remaining", remaining)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Expires", nextTime.Format(http.TimeFormat))
	w.Header().Set("Last-Modified", roundTime.Format(http.TimeFormat))
	w.Write(data)
}

func (h *handler) Group(w http.ResponseWriter, r *http.Request) {
	grp := h.group(r.Context())
	if grp == nil {
		w.WriteHeader(http.StatusNoContent)
		h.log.Warn("http_server", "failed to serve group", "client", r.RemoteAddr, "req", url.PathEscape(r.URL.Path))
		return
	}
	data, err := json.Marshal(grp.ToProto())
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		h.log.Warn("http_server", "failed to marshal group", "client", r.RemoteAddr, "req", url.PathEscape(r.URL.Path), "err", err)
		return
	}

	// Headers per recommendation for static assets at
	// https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Cache-Control
	w.Header().Set("Cache-Control", "public, max-age=604800, immutable")
	w.Header().Set("Expires", time.Now().Add(7*24*time.Hour).Format(http.TimeFormat))
	http.ServeContent(w, r, "group.json", time.Unix(grp.GenesisTime, 0), bytes.NewReader(data))
}
