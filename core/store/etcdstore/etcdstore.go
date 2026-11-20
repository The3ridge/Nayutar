// Package etcdstore implements the store contract on an etcd v3 cluster.
package etcdstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/Kaikei-e/Nayutar/core/store"
)

// probeKey is read to learn the store revision; it is never written.
const probeKey = "\x00"

// Store implements store.Store on an etcd client. It adds no key prefix of its own.
type Store struct {
	client *clientv3.Client
}

var _ store.Store = (*Store)(nil)

// New returns a store on client. The caller keeps ownership of the client.
func New(client *clientv3.Client) *Store {
	return &Store{client: client}
}

func mapErr(err error) error {
	switch {
	case errors.Is(err, rpctypes.ErrCompacted):
		return fmt.Errorf("%w: %v", store.ErrCompacted, err)
	case errors.Is(err, rpctypes.ErrFutureRev):
		return fmt.Errorf("%w: %v", store.ErrFutureRev, err)
	case errors.Is(err, rpctypes.ErrTooManyOps), errors.Is(err, rpctypes.ErrRequestTooLarge), exceedsSendLimit(err):
		return fmt.Errorf("%w: %v", store.ErrTooLarge, err)
	default:
		return err
	}
}

const (
	sendLimitPrefix       = "trying to send message larger than max"
	receiveLimitPrefix    = "received message larger than max"
	decompressLimitSubstr = "message after decompression larger than max"
	noStreamSubstr        = "no stream found"
)

// Only the message tells gRPC's message-size limits apart from etcd's other ResourceExhausted errors.
func exceedsSendLimit(err error) bool {
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.ResourceExhausted {
		return false
	}
	msg := strings.TrimPrefix(st.Message(), "grpc: ")
	return strings.HasPrefix(msg, sendLimitPrefix) ||
		strings.HasPrefix(msg, receiveLimitPrefix) ||
		strings.Contains(msg, decompressLimitSubstr)
}

// The clientv3 watcher exposes no typed error for a progress request on a stream it does not have.
func isNoStream(err error) bool {
	return strings.Contains(err.Error(), noStreamSubstr)
}

func bytesOrNil(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return b
}

func readRev(atRev store.Rev, header int64) store.Rev {
	if atRev != 0 {
		return atRev
	}
	return store.Rev(header)
}

// Get issues one etcd Get, which etcd serves as a linearizable read.
func (s *Store) Get(ctx context.Context, key string, atRev store.Rev) (store.KV, store.Rev, error) {
	if err := store.ValidateGet(key, atRev); err != nil {
		return store.KV{}, 0, err
	}
	resp, err := s.client.Get(ctx, key, clientv3.WithRev(int64(atRev)))
	if err != nil {
		return store.KV{}, 0, mapErr(err)
	}
	rev := readRev(atRev, resp.Header.Revision)
	if len(resp.Kvs) == 0 {
		return store.KV{}, rev, store.ErrNotFound
	}
	kv := resp.Kvs[0]
	return store.KV{Key: string(kv.Key), Value: bytesOrNil(kv.Value), ModRev: store.Rev(kv.ModRevision)}, rev, nil
}

// Range issues one limit-bound etcd range read per page; the continuation is the last key of the page when etcd reports more keys.
func (s *Store) Range(ctx context.Context, prefix string, atRev store.Rev, limit int, cont string) ([]store.KV, store.Rev, string, error) {
	if err := store.ValidateRange(prefix, atRev, limit, cont); err != nil {
		return nil, 0, "", err
	}
	start := prefix
	if cont != "" {
		start = cont + "\x00"
	}
	if start == "" {
		start = "\x00"
	}
	resp, err := s.client.Get(ctx, start,
		clientv3.WithRange(clientv3.GetPrefixRangeEnd(prefix)),
		clientv3.WithRev(int64(atRev)),
		clientv3.WithLimit(int64(limit)),
	)
	if err != nil {
		return nil, 0, "", mapErr(err)
	}
	var kvs []store.KV
	for _, kv := range resp.Kvs {
		kvs = append(kvs, store.KV{Key: string(kv.Key), Value: bytesOrNil(kv.Value), ModRev: store.Rev(kv.ModRevision)})
	}
	next := ""
	if resp.More && len(kvs) > 0 {
		next = kvs[len(kvs)-1].Key
	}
	return kvs, readRev(atRev, resp.Header.Revision), next, nil
}

var cmpResults = map[store.CmpOp]string{
	store.CmpEqual:    "=",
	store.CmpNotEqual: "!=",
	store.CmpLess:     "<",
	store.CmpGreater:  ">",
}

// Txn is one etcd transaction; with no ops it still goes through a quorum read.
func (s *Store) Txn(ctx context.Context, cmps []store.Cmp, ops []store.Op) (bool, store.Rev, error) {
	if err := store.ValidateTxn(cmps, ops); err != nil {
		return false, 0, err
	}
	conds := make([]clientv3.Cmp, len(cmps))
	for i, c := range cmps {
		conds[i] = clientv3.Compare(clientv3.ModRevision(c.Key), cmpResults[c.Op], int64(c.ModRev))
	}
	txn := s.client.Txn(ctx).If(conds...)
	if len(ops) == 0 {
		// Etcd treats empty Then/Else branches as vacuously serializable, skipping the quorum read.
		probe := clientv3.OpGet(probeKey, clientv3.WithCountOnly())
		txn = txn.Then(probe).Else(probe)
	} else {
		writes := make([]clientv3.Op, len(ops))
		for i, op := range ops {
			if op.Type == store.OpPut {
				writes[i] = clientv3.OpPut(op.Key, string(op.Value))
			} else {
				writes[i] = clientv3.OpDelete(op.Key)
			}
		}
		txn = txn.Then(writes...)
	}
	resp, err := txn.Commit()
	if err != nil {
		return false, 0, mapErr(err)
	}
	return resp.Succeeded, store.Rev(resp.Header.Revision), nil
}

// CurrentRev reads the cluster revision with a linearizable count-only read of a probe key.
func (s *Store) CurrentRev(ctx context.Context) (store.Rev, error) {
	resp, err := s.client.Get(ctx, probeKey, clientv3.WithCountOnly())
	if err != nil {
		return 0, mapErr(err)
	}
	return store.Rev(resp.Header.Revision), nil
}

// Compact compacts the whole etcd cluster.
func (s *Store) Compact(ctx context.Context, rev store.Rev) error {
	if err := store.ValidateCompact(rev); err != nil {
		return err
	}
	if _, err := s.client.Compact(ctx, int64(rev)); err != nil {
		return mapErr(err)
	}
	return nil
}

// Watch opens one etcd watcher per stream, which requires a leader, after a linearizable read that fixes the start
// revision (fromRev 0) or checks fromRev against compaction.
func (s *Store) Watch(ctx context.Context, prefix string, fromRev store.Rev) (store.WatchStream, error) {
	if err := store.ValidateWatch(fromRev); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var after store.Rev
	if fromRev == 0 {
		// Etcd resolves start revision 0 against the serving member's own revision, which can lag the cluster; anything at or below the linearizable revision read here predates the call.
		var err error
		after, err = s.CurrentRev(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return endedWatchStream(ctx.Err()), nil
			}
			return nil, err
		}
	} else {
		// Etcd checks historical watch compaction against the serving member's own state, so a lagging follower would miss a cluster compaction without a linearizable read first.
		if _, err := s.client.Get(ctx, probeKey, clientv3.WithRev(int64(fromRev)), clientv3.WithCountOnly()); err != nil {
			err = mapErr(err)
			switch {
			case errors.Is(err, store.ErrCompacted):
				return endedWatchStream(err), nil
			case errors.Is(err, store.ErrFutureRev):
				// Watching from a future revision is allowed.
			case ctx.Err() != nil:
				return endedWatchStream(ctx.Err()), nil
			default:
				return nil, err
			}
		}
	}
	// One etcd watcher per stream: a progress notification answers every watch
	// that shares a gRPC stream, and its revision is only meaningful for the
	// watches it was requested for.
	watcher := clientv3.NewWatcher(s.client)
	watchCtx := clientv3.WithRequireLeader(ctx)
	streamMD, _ := metadata.FromOutgoingContext(watchCtx)
	ws := &watchStream{
		watcher:  watcher,
		watchCtx: ctx,
		streamMD: streamMD,
		after:    after,
		out:      make(chan store.Event),
		done:     make(chan struct{}),
	}
	responses := watcher.Watch(watchCtx, prefix, clientv3.WithPrefix(), clientv3.WithRev(int64(fromRev)))
	go func() {
		err := ws.forward(ctx, responses)
		ws.mu.Lock()
		ws.err = err
		ws.ended = true
		ws.mu.Unlock()
		close(ws.done)
		_ = watcher.Close()
		close(ws.out)
	}()
	return ws, nil
}

type watchStream struct {
	watcher  clientv3.Watcher
	watchCtx context.Context
	streamMD metadata.MD
	after    store.Rev
	out      chan store.Event
	done     chan struct{}

	mu    sync.Mutex
	err   error
	ended bool
}

func endedWatchStream(err error) *watchStream {
	out := make(chan store.Event)
	close(out)
	done := make(chan struct{})
	close(done)
	return &watchStream{
		out:   out,
		done:  done,
		err:   err,
		ended: true,
	}
}

func (ws *watchStream) forward(ctx context.Context, responses clientv3.WatchChan) error {
	for resp := range responses {
		if err := resp.Err(); err != nil {
			return mapErr(err)
		}
		for _, ev := range resp.Events {
			rev := store.Rev(ev.Kv.ModRevision)
			if rev <= ws.after {
				continue
			}
			out := store.Event{Type: store.EventPut, Rev: rev, Key: string(ev.Kv.Key), Value: bytesOrNil(ev.Kv.Value)}
			if ev.Type == clientv3.EventTypeDelete {
				out.Type = store.EventDelete
				out.Value = nil
			}
			if err := ws.send(ctx, out); err != nil {
				return err
			}
		}
		if resp.IsProgressNotify() {
			if err := ws.send(ctx, store.Event{Type: store.EventProgress, Rev: store.Rev(resp.Header.Revision)}); err != nil {
				return err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.New("etcdstore: watch closed by the client")
}

func (ws *watchStream) send(ctx context.Context, ev store.Event) error {
	select {
	case ws.out <- ev:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (ws *watchStream) Events() <-chan store.Event { return ws.out }

func (ws *watchStream) RequestProgress(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ws.mu.Lock()
	ended, err := ws.ended, ws.err
	ws.mu.Unlock()
	if ended {
		return err
	}
	if ws.watchCtx.Err() != nil {
		select {
		case <-ws.done:
			return ws.Err()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// The etcd client picks the gRPC stream by outgoing metadata, so the
	// request must carry exactly the metadata the watch was opened with.
	reqErr := ws.watcher.RequestProgress(metadata.NewOutgoingContext(ctx, ws.streamMD))
	if reqErr == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ws.done:
		return ws.Err()
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if ws.watchCtx.Err() != nil || isNoStream(reqErr) {
		select {
		case <-ws.done:
			return ws.Err()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return mapErr(reqErr)
}

func (ws *watchStream) Err() error {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return ws.err
}
