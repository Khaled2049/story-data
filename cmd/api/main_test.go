package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

// go vet does not flag a bare http.ListenAndServe, and a missing timeout is
// invisible until someone holds a connection open, so it is asserted here.
func TestServerHasEveryTimeout(t *testing.T) {
	srv := newServer(":0", http.NewServeMux())

	for name, d := range map[string]time.Duration{
		"ReadHeaderTimeout": srv.ReadHeaderTimeout,
		"ReadTimeout":       srv.ReadTimeout,
		"WriteTimeout":      srv.WriteTimeout,
		"IdleTimeout":       srv.IdleTimeout,
	} {
		if d <= 0 {
			t.Errorf("%s is %v; a zero timeout lets one slow client hold a goroutine forever", name, d)
		}
	}
	if srv.MaxHeaderBytes <= 0 {
		t.Error("MaxHeaderBytes is unset")
	}
	// A header read must time out before the whole request does, or the
	// header limit never applies.
	if srv.ReadHeaderTimeout > srv.ReadTimeout {
		t.Errorf("ReadHeaderTimeout %v exceeds ReadTimeout %v", srv.ReadHeaderTimeout, srv.ReadTimeout)
	}
}

// A client that connects and then says nothing must be dropped rather than
// held. This is the Slowloris shape, one connection deep.
func TestServerDropsASilentClient(t *testing.T) {
	srv := newServer("127.0.0.1:0", http.NewServeMux())
	srv.ReadHeaderTimeout = 200 * time.Millisecond
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go srv.Serve(ln)
	defer srv.Close()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Half a request, then silence.
	if _, err = conn.Write([]byte("GET /health HTTP/1.1\r\nHost: x\r\n")); err != nil {
		t.Fatal(err)
	}
	if err = conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Read(make([]byte, 1)); err == nil {
		return // the server answered 408 and closed, which is also a drop
	} else if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("the server held a half-open request past ReadHeaderTimeout")
	}
}

// SIGTERM must let an in-flight request finish. Cloud Run replaces instances
// routinely, and this service commits ledger transfers.
func TestGracefulShutdownDrainsInFlightRequests(t *testing.T) {
	started := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("done"))
	})

	srv := newServer("127.0.0.1:0", mux)
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)

	result := make(chan int, 1)
	go func() {
		res, e := http.Get("http://" + ln.Addr().String() + "/slow")
		if e != nil {
			result <- 0
			return
		}
		res.Body.Close()
		result <- res.StatusCode
	}()

	<-started
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err = srv.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if status := <-result; status != http.StatusOK {
		t.Errorf("in-flight request finished with %d, want 200", status)
	}
}
