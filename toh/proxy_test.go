package toh

import (
	"bufio"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"runtime/pprof"
	"strings"
	"testing"
	"time"
)

func iocopy(dst io.Writer, src io.Reader) (written int64, err error) {
	size := 32 * 1024
	buf := make([]byte, size)
	for {
		nr, er := src.Read(buf)
		if nr > 0 {
			//	vprint(string(buf[:1024]))
			nw, ew := dst.Write(buf[0:nr])
			if nw > 0 {
				written += int64(nw)
			}
			if ew != nil {
				err = ew
				break
			}
			if nr != nw {
				err = io.ErrShortWrite
				break
			}
		}
		if er != nil {
			if er != io.EOF {
				err = er
			}
			break
		}
	}
	return written, err
}

type client int

type server int

func (s *client) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if !strings.Contains(host, ":") {
		host += ":80"
	}

	up, _ := Dial("tcp", ":10001")
	up.Write([]byte(r.Method[:1] + host + "\n"))

	down, _, _ := w.(http.Hijacker).Hijack()
	if r.Method != "CONNECT" {
		header, _ := httputil.DumpRequestOut(r, false)
		x := string(header)
		up.Write([]byte(x))
		io.Copy(up, r.Body)
	}

	go func() { io.Copy(up, down) }()
	go func() { iocopy(down, up) }()
}

func foo(conn net.Conn) {
	down := bufio.NewReader(conn)
	buf, _ := down.ReadBytes('\n')
	host := string(buf)
	connect := host[0] == 'C'
	host = host[1 : len(host)-1]
	vprint(host, connect)

	up, _ := net.Dial("tcp", host)

	if up == nil || down == nil {
		conn.Write([]byte("HTTP/1.1 503 Service Unavailable\r\n\r\n"))
		return
	}

	if connect {
		conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	}

	go func() { io.Copy(up, down) }()
	go func() { io.Copy(conn, up) }()
}

func TestProxy(t *testing.T) {

	go func() {
		for {
			time.Sleep(2 * time.Second)
			f, _ := os.Create("heap.txt")
			pprof.Lookup("goroutine").WriteTo(f, 1)
			//fmt.Println("profile")
		}
	}()

	//p, _ := url.Parse("68.183.156.72:8080")
	//DefaultTransport.Proxy = http.ProxyURL(p)
	DefaultTransport.MaxConnsPerHost = 10

	go func() {
		log.Println("hello")
		go http.ListenAndServe(":10000", new(client))

		ln, _ := Listen("tcp", ":10001")
		for {
			conn, _ := ln.Accept()
			go foo(conn)
		}
	}()

	//	Verbose = false
	select {}
}
