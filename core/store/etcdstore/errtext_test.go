package etcdstore

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/encoding/gzip"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/Kaikei-e/Nayutar/core/store"
)

// These tests take the error texts etcdstore matches on from the real grpc and
// clientv3 libraries, so a dependency bump that rewords them fails here.

const (
	smallLimit = 200
	bigSize    = 4000
)

type kvServer struct {
	pb.UnimplementedKVServer
	respond func(*pb.RangeRequest) (*pb.RangeResponse, error)
}

func (s *kvServer) Range(_ context.Context, r *pb.RangeRequest) (*pb.RangeResponse, error) {
	return s.respond(r)
}

func newKVClient(t *testing.T, respond func(*pb.RangeRequest) (*pb.RangeResponse, error), serverOpts []grpc.ServerOption, dialOpts []grpc.DialOption) pb.KVClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(serverOpts...)
	pb.RegisterKVServer(srv, &kvServer{respond: respond})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	opts := append([]grpc.DialOption{
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}, dialOpts...)
	conn, err := grpc.NewClient("passthrough:///bufnet", opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return pb.NewKVClient(conn)
}

func rangeWith(t *testing.T, kv pb.KVClient, keySize int, callOpts ...grpc.CallOption) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := kv.Range(ctx, &pb.RangeRequest{Key: []byte(strings.Repeat("k", keySize))}, callOpts...)
	return err
}

func TestExceedsSendLimitRecognisesGRPCLimitErrors(t *testing.T) {
	bigRange := func(*pb.RangeRequest) (*pb.RangeResponse, error) {
		return &pb.RangeResponse{
			Header: &pb.ResponseHeader{ClusterId: 1},
			Kvs:    []*mvccpb.KeyValue{{Key: []byte("a"), Value: []byte(strings.Repeat("v", bigSize))}},
			Count:  1,
		}, nil
	}
	small := func(*pb.RangeRequest) (*pb.RangeResponse, error) {
		return &pb.RangeResponse{Header: &pb.ResponseHeader{ClusterId: 1}}, nil
	}
	cases := []struct {
		name    string
		wantSub string
		run     func(t *testing.T) error
	}{
		{
			name:    "client send too large",
			wantSub: sendLimitPrefix,
			run: func(t *testing.T) error {
				kv := newKVClient(t, small, nil, nil)
				return rangeWith(t, kv, bigSize, grpc.MaxCallSendMsgSize(smallLimit))
			},
		},
		{
			name:    "client receive too large",
			wantSub: receiveLimitPrefix,
			run: func(t *testing.T) error {
				kv := newKVClient(t, bigRange, nil, nil)
				return rangeWith(t, kv, 1, grpc.MaxCallRecvMsgSize(smallLimit))
			},
		},
		{
			name:    "server receive too large",
			wantSub: receiveLimitPrefix,
			run: func(t *testing.T) error {
				kv := newKVClient(t, small, []grpc.ServerOption{grpc.MaxRecvMsgSize(smallLimit)}, nil)
				return rangeWith(t, kv, bigSize)
			},
		},
		{
			name:    "server send too large",
			wantSub: sendLimitPrefix,
			run: func(t *testing.T) error {
				kv := newKVClient(t, bigRange, []grpc.ServerOption{grpc.MaxSendMsgSize(smallLimit)}, nil)
				return rangeWith(t, kv, 1)
			},
		},
		{
			name:    "server receive too large after decompression",
			wantSub: decompressLimitSubstr,
			run: func(t *testing.T) error {
				// Repetitive bytes compress well below the limit and expand far above it.
				kv := newKVClient(t, small, []grpc.ServerOption{grpc.MaxRecvMsgSize(smallLimit)}, nil)
				return rangeWith(t, kv, 100000, grpc.UseCompressor(gzip.Name))
			},
		},
		{
			name:    "client receive too large after decompression",
			wantSub: decompressLimitSubstr,
			run: func(t *testing.T) error {
				bigCompressible := func(*pb.RangeRequest) (*pb.RangeResponse, error) {
					return &pb.RangeResponse{
						Header: &pb.ResponseHeader{ClusterId: 1},
						Kvs:    []*mvccpb.KeyValue{{Key: []byte("a"), Value: []byte(strings.Repeat("v", 100000))}},
						Count:  1,
					}, nil
				}
				kv := newKVClient(t, bigCompressible, nil, nil)
				return rangeWith(t, kv, 1, grpc.UseCompressor(gzip.Name), grpc.MaxCallRecvMsgSize(2*smallLimit+bigSize))
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run(t)
			if err == nil {
				t.Fatal("expected a size limit error")
			}
			if got := status.Code(err); got != codes.ResourceExhausted {
				t.Fatalf("code = %v, want ResourceExhausted (err: %v)", got, err)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not contain %q", err, tc.wantSub)
			}
			if !exceedsSendLimit(err) {
				t.Errorf("exceedsSendLimit does not recognise %q", err)
			}
			if !errors.Is(mapErr(err), store.ErrTooLarge) {
				t.Errorf("mapErr(%q) is not ErrTooLarge", err)
			}
		})
	}
}

func TestExceedsSendLimitRejectsOtherResourceExhausted(t *testing.T) {
	for _, tc := range []struct {
		name string
		sent error
	}{
		{"no space", rpctypes.ErrGRPCNoSpace},
		{"too many requests", rpctypes.ErrGRPCRequestTooManyRequests},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kv := newKVClient(t, func(*pb.RangeRequest) (*pb.RangeResponse, error) { return nil, tc.sent }, nil, nil)
			err := rangeWith(t, kv, 1)
			if status.Code(err) != codes.ResourceExhausted {
				t.Fatalf("code = %v, want ResourceExhausted (err: %v)", status.Code(err), err)
			}
			for name, e := range map[string]error{"raw": err, "converted": rpctypes.Error(err)} {
				if exceedsSendLimit(e) {
					t.Errorf("%s: exceedsSendLimit recognises %q", name, e)
				}
				if errors.Is(mapErr(e), store.ErrTooLarge) {
					t.Errorf("%s: mapErr(%q) is ErrTooLarge", name, e)
				}
			}
		})
	}
}

type stubWatchClient struct{ pb.WatchClient }

func TestNoStreamErrorIsRecognised(t *testing.T) {
	w := clientv3.NewWatchFromWatchClient(stubWatchClient{}, nil)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	err := w.RequestProgress(context.Background())
	if err == nil {
		t.Fatal("RequestProgress on a closed watcher succeeded")
	}
	if !isNoStream(err) {
		t.Fatalf("isNoStream does not recognise %q", err)
	}
	if isNoStream(context.Canceled) {
		t.Error("isNoStream recognises context.Canceled")
	}
}
