package main

import (
	"fmt"
	"io/ioutil"
	"math/rand"
	"net"
	"net/http"
	"os"
	"runtime/pprof"
	"sync"
	"testing"
	"time"

	"github.com/coyove/tcpmux"
)

func randomString() string {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	buf := make([]byte, 16+r.Intn(10))
	for i := 0; i < len(buf); i++ {
		buf[i] = byte(r.Intn(26)) + 'a'
	}
	return string(buf)
}

// go test -v -timeout 20m
func zTestHTTPServer(t *testing.T) {
	// go func() {
	// 	log.Println(http.ListenAndServe("localhost:6060", nil))
	// }()

	go func() {
		ln := getListerner()

		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			// http library tend to reuse the conn, but in this test we don't
			h := w.(http.Hijacker)
			conn, _, _ := h.Hijack()

			res := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n", len(r.RequestURI[1:]))
			conn.Write([]byte(res + r.RequestURI[1:]))
			conn.Close()
			// n, err := conn.Read(buf)
			// if err == nil {
			// 	logg.D(buf[:n])
			// }
			// w.Write([]byte(r.RequestURI[1:]))
		})
		http.Serve(ln, nil)
	}()

	num := 100
	// streamMapping := make(map[uint32]*tcpmux.Stream)
	// smLock := sync.Mutex{}
	p := tcpmux.NewDialer("127.0.0.1:13739", num)

	client := http.Client{
		Transport: &http.Transport{
			Dial: func(network, addr string) (net.Conn, error) {
				s, err := p.Dial()
				s.(*tcpmux.Stream).SetStreamOpt(tcpmux.OptErrWhenClosed)
				return s, err
			},
		},
	}

	test := func(wg *sync.WaitGroup) {
		str := randomString()
		resp, err := client.Get("http://127.0.0.1:13739/" + str)

		if err != nil {
			// time.Sleep(time.Second)
			// for _, s := range streamMapping {
			// 	logg.D(s.streamIdx)
			// 	for len(s.readResp) > 0 {
			// 		logg.D(<-s.readResp)
			// 	}

			// 	logg.D(s.lastResp)
			// 	logg.D("==================")
			// }
			panic(err)
		}

		buf, _ := ioutil.ReadAll(resp.Body)
		if string(buf) != str {
			panic(string(buf))
		}

		resp.Body.Close()
		wg.Done()
	}

	go func() {
		for {
			time.Sleep(2 * time.Second)
			//p.conns[1].conn.Close()
			f, _ := os.Create("heap.txt")
			pprof.Lookup("heap").WriteTo(f, 1)
			// debug.WriteHeapDump(f.Fd())
		}
	}()

	for {
		wg := &sync.WaitGroup{}

		// start := time.Now()
		//streamMapping = make(map[uint32]*tcpmux.Stream)
		for i := 0; i < num*10; i++ {
			wg.Add(1)
			go test(wg)
		}
		wg.Wait()
		// logg.D("take: ", time.Now().Sub(start).Seconds())

		//logg.D(p.Count())
	}

	select {}
}
