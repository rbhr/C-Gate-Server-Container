package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestHealthAndReadinessSplit covers what /health and /ready each promise.
//
// /health stays 200 while the bridge is serving: it is what decides whether to
// restart the container, and C-Gate needs up to a minute to sync its networks
// on a cold start, so failing it during that window would turn a normal boot
// into a restart loop. /ready is the probe that reports whether C-Gate is
// actually reachable.
func TestHealthAndReadinessSplit(t *testing.T) {
	set := func(e, s, c bool) {
		eventStreamUp.Store(e)
		statusStreamUp.Store(s)
		commandUp.Store(c)
	}
	t.Cleanup(func() { set(false, false, false) })

	type body struct {
		Status      string          `json:"status"`
		Connections map[string]bool `json:"connections"`
	}

	get := func(t *testing.T, h http.HandlerFunc) (int, body) {
		t.Helper()
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, "http://bridge:8980/x", nil))
		var b body
		if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
			t.Fatalf("decoding %q: %v", rec.Body.String(), err)
		}
		return rec.Code, b
	}

	t.Run("nothing connected", func(t *testing.T) {
		set(false, false, false)

		code, b := get(t, handleHealth)
		if code != http.StatusOK {
			t.Errorf("health = %d, want 200 even with C-Gate down", code)
		}
		if b.Status != "degraded" {
			t.Errorf("health status = %q, want degraded", b.Status)
		}

		if code, _ := get(t, handleReady); code != http.StatusServiceUnavailable {
			t.Errorf("ready = %d, want 503", code)
		}
	})

	t.Run("partially connected", func(t *testing.T) {
		// The command port is the one /cgate needs; the streams being up is
		// not enough to call the bridge ready.
		set(true, true, false)

		if code, _ := get(t, handleHealth); code != http.StatusOK {
			t.Errorf("health = %d, want 200", code)
		}
		code, b := get(t, handleReady)
		if code != http.StatusServiceUnavailable {
			t.Errorf("ready with the command port down = %d, want 503", code)
		}
		if b.Connections["command"] {
			t.Error("body reports the command port up when it is not")
		}
	})

	t.Run("fully connected", func(t *testing.T) {
		set(true, true, true)

		code, b := get(t, handleReady)
		if code != http.StatusOK {
			t.Errorf("ready = %d, want 200", code)
		}
		if b.Status != "ok" {
			t.Errorf("status = %q, want ok", b.Status)
		}
	})
}

// TestCommandDuringOutageFailsRatherThanHanging is the point of splitting the
// dial in two.
//
// connect() used to retry forever with s.mu held, so a C-Gate that was away for
// minutes — a restart, a project reload, a reboot — queued every /cgate request
// behind one dial for the whole outage. This is the regression to guard: if a
// change reintroduces a retry loop under the lock, this test hangs.
func TestCommandDuringOutageFailsRatherThanHanging(t *testing.T) {
	// Nothing is listening on the command port under test, so the dial fails
	// the way it would during an outage.
	s := &commandSession{}
	commandUp.Store(false)
	t.Cleanup(func() { commandUp.Store(false) })

	done := make(chan error, 1)
	go func() {
		_, err := s.send("noop")
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("send succeeded with no C-Gate listening")
		}
	case <-time.After(dialTimeout + 5*time.Second):
		t.Fatal("send blocked past the dial timeout — it is retrying under the lock again")
	}

	if commandUp.Load() {
		t.Error("a failed dial left the session marked up")
	}

	// The mutex has to be free for the next caller.
	if !s.mu.TryLock() {
		t.Error("send left s.mu held")
	} else {
		s.mu.Unlock()
	}
}
