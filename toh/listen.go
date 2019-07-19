package toh

import (
	"crypto/aes"
	"crypto/cipher"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

type Listener struct {
	ln           net.Listener
	closed       bool
	conns        map[uint64]*ServerConn
	connsmu      sync.Mutex
	httpServeErr chan error
	pendingConns chan *ServerConn
	blk          cipher.Block
}

func (l *Listener) Close() error {
	select {
	case l.httpServeErr <- fmt.Errorf("accept on closed listener"):
	}
	l.closed = true
	return l.ln.Close()
}

func (l *Listener) Addr() net.Addr {
	return l.ln.Addr()
}

func (l *Listener) Accept() (net.Conn, error) {
	for {
		select {
		case err := <-l.httpServeErr:
			return nil, err
		case conn := <-l.pendingConns:
			return conn, nil
		}
	}
}

func Listen(network string, address string) (net.Listener, error) {
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}

	l := &Listener{
		ln:           ln,
		httpServeErr: make(chan error, 1),
		pendingConns: make(chan *ServerConn, 1024),
		conns:        map[uint64]*ServerConn{},
	}

	l.blk, _ = aes.NewCipher([]byte(network + "0123456789abcdef")[:16])

	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/", l.handler)
		l.httpServeErr <- http.Serve(ln, mux)
	}()

	if Verbose {
		go func() {
			for range time.Tick(time.Second * 5) {
				ln := 0
				l.connsmu.Lock()
				for _, conn := range l.conns {
					ln += len(conn.write.buf)
					//vprint(conn, len(conn.write.buf))
				}
				l.connsmu.Unlock()
				vprint("listener active connections: ", len(l.conns), ", pending bytes: ", ln)
			}
		}()
	}

	return l, nil
}

type Dialer struct {
	endpoint string
	orch     chan *ClientConn
}

func NewDialer(endpoint string) *Dialer {
	d := &Dialer{
		endpoint: endpoint,
		orch:     make(chan *ClientConn, 128),
	}
	d.start()
	return d
}
