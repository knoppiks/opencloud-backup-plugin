package notify

import (
	"bufio"
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// listen starts a TCP listener whose connections are handled by serve.
func listen(t *testing.T, serve func(net.Conn)) (host string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var wg sync.WaitGroup
	t.Cleanup(func() {
		_ = ln.Close()
		wg.Wait()
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { _ = conn.Close() }()
				serve(conn)
			}()
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

// silent accepts and never answers, until the client hangs up.
func silent(conn net.Conn) { _, _ = io.Copy(io.Discard, conn) }

func operatorEvent() Event {
	return Event{Kind: KindRunFailed, Audience: AudienceOperator, Message: "a run failed"}
}

func sinkFor(t *testing.T, host string, port int, timeout time.Duration) *SMTPSink {
	t.Helper()
	sink, err := NewSMTPSink(SMTPConfig{
		Host: host, Port: port, From: "backup@example.org", OperatorTo: "admin@example.org",
		Timeout: timeout,
	})
	if err != nil {
		t.Fatalf("NewSMTPSink: %v", err)
	}
	return sink
}

// A mail server that accepts and then says nothing must not hold a scheduler
// slot: net/smtp.SendMail would wait forever (review-2026-10.md F7).
func TestSMTPSink_ASilentServerTimesOut(t *testing.T) {
	host, port := listen(t, silent)

	sink := sinkFor(t, host, port, 200*time.Millisecond)
	start := time.Now()
	err := sink.Deliver(context.Background(), operatorEvent())
	if err == nil {
		t.Fatal("Deliver to a silent server succeeded")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Deliver took %v, want it bounded by the timeout", elapsed)
	}
}

func TestSMTPSink_CancellationEndsADelivery(t *testing.T) {
	host, port := listen(t, silent)

	sink := sinkFor(t, host, port, time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := sink.Deliver(ctx, operatorEvent()); err == nil {
		t.Fatal("Deliver succeeded after cancellation")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Deliver took %v, want it to end with the context", elapsed)
	}
}

// fakeSMTP speaks just enough SMTP for one plain delivery and records the
// message.
func fakeSMTP(received chan<- string) func(net.Conn) {
	return func(conn net.Conn) {
		r := bufio.NewReader(conn)
		reply := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
		reply("220 fake ESMTP")
		var data strings.Builder
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if inData {
				if line == ".\r\n" {
					inData = false
					received <- data.String()
					reply("250 queued")
					continue
				}
				data.WriteString(line)
				continue
			}
			switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				reply("250 fake")
			case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"):
				reply("250 ok")
			case cmd == "DATA":
				inData = true
				reply("354 go ahead")
			case cmd == "QUIT":
				reply("221 bye")
				return
			default:
				reply("502 unknown")
			}
		}
	}
}

func TestSMTPSink_DeliversThroughARealConversation(t *testing.T) {
	received := make(chan string, 1)
	host, port := listen(t, fakeSMTP(received))

	sink := sinkFor(t, host, port, 5*time.Second)
	if err := sink.Deliver(context.Background(), operatorEvent()); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	select {
	case msg := <-received:
		if !strings.Contains(msg, "a run failed") || !strings.Contains(msg, "To: admin@example.org") {
			t.Fatalf("message = %q", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no message received")
	}
}

// A server that refuses the sender is reported by step and reply code, and its
// reply text is not repeated: a reply can quote what the client sent
// (review-2026-10.md F3).
func TestSMTPSink_ARefusalNamesTheStepAndCodeOnly(t *testing.T) {
	host, port := listen(t, func(conn net.Conn) {
		r := bufio.NewReader(conn)
		reply := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
		reply("220 fake ESMTP")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				reply("250 fake")
			case strings.HasPrefix(cmd, "MAIL"):
				reply("553 sender quoted-server-text refused")
			default:
				reply("221 bye")
				return
			}
		}
	})

	err := sinkFor(t, host, port, 5*time.Second).Deliver(context.Background(), operatorEvent())
	if err == nil {
		t.Fatal("Deliver succeeded against a refusing server")
	}
	if msg := err.Error(); !strings.Contains(msg, "mail from: server replied 553") {
		t.Fatalf("error = %q, want the step and the reply code", msg)
	}
	if strings.Contains(err.Error(), "quoted-server-text") {
		t.Fatalf("error repeats the server's reply text: %v", err)
	}
}

// A mail server that is not there is reported as a connection failure, with
// the address the operator configured.
func TestSMTPSink_AnUnreachableServerSaysSo(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	_ = ln.Close()

	err = sinkFor(t, addr.IP.String(), addr.Port, 5*time.Second).Deliver(context.Background(), operatorEvent())
	if err == nil {
		t.Fatal("Deliver succeeded against a closed port")
	}
	if msg := err.Error(); !strings.Contains(msg, "connect: ") || !strings.Contains(msg, addr.String()) {
		t.Fatalf("error = %q, want a connection failure naming %s", msg, addr)
	}
}

func TestSMTPSink_ATimeoutSaysSo(t *testing.T) {
	host, port := listen(t, silent)
	err := sinkFor(t, host, port, 200*time.Millisecond).Deliver(context.Background(), operatorEvent())
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %v, want a timeout", err)
	}
}
