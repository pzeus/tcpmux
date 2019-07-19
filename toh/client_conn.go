package toh

import (
	"crypto/aes"
	"crypto/cipher"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coyove/common/sched"
)

var (
	DefaultTransport = &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
			DualStack: true,
		}).DialContext,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	InactivePurge      = time.Minute
	ClientReadTimeout  = time.Second * 15
	MaxWriteBufferSize = 1024 * 1024 * 1
	MaxReadBufferSize  = 1024 * 1024 * 1
	OnRequestServer    = func() *http.Transport { return DefaultTransport }
)

type respNode struct {
	r io.ReadCloser
	f frame
}

type ClientConn struct {
	idx      uint64
	endpoint string

	write struct {
		sync.Mutex
		counter uint32
		sched   sched.SchedKey
		buf     []byte
		survey  struct {
			lastIsPositive bool
			pendingSize    int
			reschedCount   int64
		}
		respCh     chan respNode
		respChOnce sync.Once
	}

	read *readConn
}

func Dial(network string, address string) (net.Conn, error) {
	blk, _ := aes.NewCipher([]byte(network + "0123456789abcdef")[:16])
	c, err := newClientConn("http://"+address+"/", blk)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func newClientConn(endpoint string, blk cipher.Block) (*ClientConn, error) {
	c := &ClientConn{endpoint: endpoint}
	c.idx = newConnectionIdx()
	c.write.survey.pendingSize = 1
	c.write.respCh = make(chan respNode, 16)
	c.read = newReadConn(c.idx, blk, 'c')

	// Say hello
	resp, err := c.send(frame{
		idx:     rand.Uint32(),
		connIdx: c.idx,
		options: optSyncConnIdx,
		next: &frame{
			connIdx: c.idx,
			options: optHello,
		}})
	if err != nil {
		return nil, err
	}
	resp.Body.Close()

	c.write.sched = sched.Schedule(c.schedSending, time.Second)

	go c.respLoop()
	go c.respLoop()
	return c, nil
}

func (c *ClientConn) SetDeadline(t time.Time) error {
	c.SetReadDeadline(t)
	return nil
}

func (c *ClientConn) SetReadDeadline(t time.Time) error {
	c.read.ready.SetWaitDeadline(t)
	return nil
}

func (c *ClientConn) SetWriteDeadline(t time.Time) error {
	return nil
}

func (c *ClientConn) LocalAddr() net.Addr {
	return &net.TCPAddr{}
}

func (c *ClientConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{}
}

func (c *ClientConn) Close() error {
	vprint(c, " closing")
	c.write.sched.Cancel()
	c.read.close()
	c.write.respChOnce.Do(func() {
		close(c.write.respCh)
		go c.send(frame{
			connIdx: c.idx,
			options: optClosed,
		})
	})
	return nil
}

func (c *ClientConn) Write(p []byte) (n int, err error) {
REWRITE:
	if c.read.err != nil {
		return 0, c.read.err
	}

	if c.read.closed {
		return 0, ErrClosedConn
	}

	if len(c.write.buf) > MaxWriteBufferSize {
		vprint("write buffer is full")
		time.Sleep(time.Second)
		goto REWRITE
	}

	c.write.Lock()
	c.write.sched.Reschedule(func() {
		c.write.survey.pendingSize = 1
		c.schedSending()
	}, time.Second)
	c.write.buf = append(c.write.buf, p...)
	c.write.Unlock()

	if len(c.write.buf) < c.write.survey.pendingSize {
		return len(p), nil
	}

	c.schedSending()
	return len(p), nil
}

func (c *ClientConn) schedSending() {
	atomic.AddInt64(&c.write.survey.reschedCount, 1)

	if c.read.err != nil || c.read.closed {
		c.Close()
		return
	}

	orchSendWriteBuf(c)
	c.write.sched.Reschedule(func() {
		c.write.survey.pendingSize = 1
		c.schedSending()
	}, time.Second)
}

func (c *ClientConn) sendWriteBuf() {
	c.write.Lock()
	defer c.write.Unlock()

	if c.write.survey.pendingSize *= 2; c.write.survey.pendingSize > 1024 {
		c.write.survey.pendingSize = 1024
	}

	if c.read.err != nil {
		return
	}

	f := frame{
		idx:     rand.Uint32(),
		connIdx: c.idx,
		options: optSyncConnIdx,
		next: &frame{
			idx:     c.write.counter + 1,
			connIdx: c.idx,
			data:    c.write.buf,
		},
	}

	deadline := time.Now().Add(InactivePurge - time.Second)
	for {
		if resp, err := c.send(f); err != nil {
			if time.Now().After(deadline) {
				c.read.feedError(err)
				return
			}
		} else {
			c.write.buf = c.write.buf[:0]
			c.write.counter++
			func() {
				defer func() { recover() }()
				c.write.respCh <- respNode{r: resp.Body}
			}()
			break
		}
	}
}

func (c *ClientConn) send(f frame) (resp *http.Response, err error) {
	client := &http.Client{
		Timeout:   time.Second * 15,
		Transport: OnRequestServer(),
	}

	req, _ := http.NewRequest("POST", c.endpoint, f.marshal(c.read.blk))
	resp, err = client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("remote is unavailable: %s", resp.Status)
	}
	return resp, nil
}

func (c *ClientConn) respLoop() {
	for {
		select {
		case body, ok := <-c.write.respCh:
			if !ok {
				return
			}
			if body.r != nil {
				k := sched.Schedule(func() { body.r.Close() }, ClientReadTimeout)
				if n, _ := c.read.feedframes(body.r); n == 0 {
					c.write.survey.lastIsPositive = false
				}
				k.Cancel()
				body.r.Close()
			} else {
				if c.read.err == nil && !c.read.closed {
					vprint(c, body.f.idx)
					c.read.feedframe(body.f)
				}
			}
		} // end of select
	}
}

func (c *ClientConn) Read(p []byte) (n int, err error) {
	return c.read.Read(p)
}

func (c *ClientConn) String() string {
	return fmt.Sprintf("<ClientConn:%x,r:%d,w:%d>", c.idx, c.read.counter, c.write.counter)
}
