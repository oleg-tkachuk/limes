package pgstore

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes"
)

// heartbeatForTests is the watcher's heartbeat in these checks: short, so a
// dead connection is found within a check.
const heartbeatForTests = 100 * time.Millisecond

// quietBeats is how many heartbeats a live, quiet connection is watched
// through.
const quietBeats = 3

// proxyBuffer is the size of each read the proxy relays.
const proxyBuffer = 32 << 10

// freezingProxy relays TCP to target until frozen, then relays nothing and
// keeps every socket open: a database gone without a word, as a host that
// vanished or a network cut leaves it.
type freezingProxy struct {
	ln     net.Listener
	frozen atomic.Bool
	mu     sync.Mutex
	conns  []net.Conn
}

func newFreezingProxy(t *testing.T, target string) *freezingProxy {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &freezingProxy{ln: ln}
	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			server, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", target)
			if err != nil {
				_ = client.Close()
				continue
			}
			p.mu.Lock()
			p.conns = append(p.conns, client, server)
			p.mu.Unlock()
			go p.relay(client, server)
			go p.relay(server, client)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		p.mu.Lock()
		defer p.mu.Unlock()
		for _, c := range p.conns {
			_ = c.Close()
		}
	})
	return p
}

// relay copies src to dst, dropping what it reads once frozen.
func (p *freezingProxy) relay(src, dst net.Conn) {
	buf := make([]byte, proxyBuffer)
	for {
		n, err := src.Read(buf)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				_ = dst.Close()
			}
			return
		}
		if p.frozen.Load() {
			continue
		}
		if _, err := dst.Write(buf[:n]); err != nil {
			return
		}
	}
}

// connect opens a connection as the runtime role through the proxy.
func (p *freezingProxy) connect(ctx context.Context) (*pgx.Conn, error) {
	cfg := owner.Config().ConnConfig.Copy()
	cfg.User, cfg.Password = appUser, appPassword
	host, port, err := net.SplitHostPort(p.ln.Addr().String())
	if err != nil {
		return nil, err
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return nil, err
	}
	cfg.Host, cfg.Port, cfg.Fallbacks = host, uint16(n), nil
	return pgx.ConnectConfig(ctx, cfg)
}

// A quiet connection that answers its heartbeats keeps vouching for the
// cache; one that stops answering, though its socket stays open, ends as a
// lost one does, so the cache serves its short TTL within a few heartbeats
// rather than once TCP gives up.
func TestRevocationWatcherHeartbeatFindsADeadConnection(t *testing.T) {
	f := newTenantFixture(t)
	cfg := owner.Config().ConnConfig
	proxy := newFreezingProxy(t, net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port))))
	cache, err := limes.NewRevocationCache(limes.RevocationCacheConfig{Capabilities: f.records, TTL: unsyncedCacheTTL, SyncedTTL: cacheTTL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		_ = RevocationWatcher{Connect: proxy.connect, Caches: []Clearer{cache}, Heartbeat: heartbeatForTests}.Run(ctx)
	}()
	await := func(what string, cond func(limes.RevocationCacheStats) bool) {
		t.Helper()
		deadline := time.Now().Add(watchWait)
		for !cond(cache.Stats()) {
			if time.Now().After(deadline) {
				t.Fatalf("%s: not within %s", what, watchWait)
			}
			time.Sleep(heartbeatForTests / 10)
		}
	}
	await("listening", func(st limes.RevocationCacheStats) bool { return st.Synced })
	time.Sleep(quietBeats * heartbeatForTests)
	if st := cache.Stats(); !st.Synced || st.Syncs != 1 || st.SyncsLost != 0 {
		t.Fatalf("a live quiet connection: Stats = %+v; want still synced on its first connection", st)
	}
	proxy.frozen.Store(true)
	await("the dead connection found", func(st limes.RevocationCacheStats) bool { return !st.Synced && st.SyncsLost >= 1 })
}

// A negative heartbeat is refused at once.
func TestRevocationWatcherRefusesANegativeHeartbeat(t *testing.T) {
	err := RevocationWatcher{Connect: connectApp, Heartbeat: -heartbeatForTests}.Run(t.Context())
	if !errors.Is(err, errNegativeHeartbeat) {
		t.Errorf("Run with a negative heartbeat = %v, want errNegativeHeartbeat", err)
	}
}
