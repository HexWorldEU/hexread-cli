package client

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestConvertSyncInline - a 200 returns the inline result; the file is streamed as multipart and
// the auto Idempotency-Key is sent.
func TestConvertSyncInline(t *testing.T) {
	var gotFile, gotIdem string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIdem = r.Header.Get("Idempotency-Key")
		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			if p.FormName() == "file" {
				b, _ := io.ReadAll(p)
				gotFile = string(b)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"markdown":"# Hi","pages":[{"index":0,"markdown":"# Hi"}],"meta":{"pages":1}}`))
	}))
	defer srv.Close()

	resp, err := New(srv.URL+"/v1", "k").Convert(context.Background(), strings.NewReader("PDFBYTES"),
		ConvertOptions{Filename: "doc.pdf", IdempotencyKey: "idem-1"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Inline == nil || resp.Inline.Markdown != "# Hi" {
		t.Fatalf("inline = %+v, want markdown", resp.Inline)
	}
	if gotFile != "PDFBYTES" {
		t.Fatalf("server got file %q, want the streamed bytes", gotFile)
	}
	if gotIdem != "idem-1" {
		t.Fatalf("Idempotency-Key = %q, want idem-1", gotIdem)
	}
}

// TestConvertForwardsModel - the --model selection is written as a `model` form field, and the
// returned meta surfaces the chosen model + route reason.
func TestConvertForwardsModel(t *testing.T) {
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			if p.FormName() == "model" {
				b, _ := io.ReadAll(p)
				gotModel = string(b)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"markdown":"# Hi","pages":[],"meta":{"pages":1,"model":"paddleocr-vl","route_reason":"scanned_or_photo"}}`))
	}))
	defer srv.Close()

	resp, err := New(srv.URL+"/v1", "k").Convert(context.Background(), strings.NewReader("x"),
		ConvertOptions{Filename: "doc.pdf", Model: "paddleocr-vl"})
	if err != nil {
		t.Fatal(err)
	}
	if gotModel != "paddleocr-vl" {
		t.Fatalf("server got model %q, want paddleocr-vl", gotModel)
	}
	if resp.Inline == nil || resp.Inline.Meta.Model != "paddleocr-vl" || resp.Inline.Meta.RouteReason != "scanned_or_photo" {
		t.Fatalf("meta = %+v, want model+route_reason surfaced", resp.Inline.Meta)
	}
}

// TestConvertOmitsModelWhenEmpty - no `model` field is sent when the flag is unset.
func TestConvertOmitsModelWhenEmpty(t *testing.T) {
	sawModel := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			if p.FormName() == "model" {
				sawModel = true
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"markdown":"x","pages":[],"meta":{"pages":1}}`))
	}))
	defer srv.Close()
	if _, err := New(srv.URL+"/v1", "k").Convert(context.Background(), strings.NewReader("x"), ConvertOptions{}); err != nil {
		t.Fatal(err)
	}
	if sawModel {
		t.Fatal("model field should be omitted when unset")
	}
}

// TestConvertUnblocksOnCancel - canceling the context mid-upload returns promptly even when the
// source reader is blocked (e.g. an idle stdin). Regression: the transport's write loop stayed
// parked in the body read, so Convert - and the whole CLI - hung past Ctrl-C.
func TestConvertUnblocksOnCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Never respond; consuming the body blocks (the client sends nothing) and errors out
		// once the canceled client tears the connection down, letting the handler exit.
		_, _ = io.Copy(io.Discard, r.Body)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	release := make(chan struct{})
	defer close(release)
	done := make(chan error, 1)
	go func() {
		_, err := New(srv.URL+"/v1", "k").Convert(ctx, blockingReader{release}, ConvertOptions{})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("want an error from the canceled convert")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Convert did not return after context cancellation (upload hang)")
	}
}

// blockingReader blocks every Read until released - a stand-in for an idle stdin.
type blockingReader struct{ release chan struct{} }

func (b blockingReader) Read([]byte) (int, error) { <-b.release; return 0, io.EOF }

// TestConvertAsync202 - a 202 returns the async job.
func TestConvertAsync202(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"job_id":"job_9","status":"queued","links":{"events":"/v1/jobs/job_9/events"}}`))
	}))
	defer srv.Close()
	resp, err := New(srv.URL+"/v1", "k").Convert(context.Background(), strings.NewReader("x"), ConvertOptions{})
	if err != nil || resp.Job == nil || resp.Job.JobID != "job_9" {
		t.Fatalf("async = %+v, %v, want job_9", resp, err)
	}
}

// TestConvertErrorTyped - a non-2xx is a typed *APIError (so the CLI maps the exit code).
func TestConvertErrorTyped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_, _ = w.Write([]byte(`{"type":"payload_too_large","code":"file_too_large"}`))
	}))
	defer srv.Close()
	_, err := New(srv.URL+"/v1", "k").Convert(context.Background(), strings.NewReader("x"), ConvertOptions{})
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != "file_too_large" || ae.Status != 413 {
		t.Fatalf("err = %v, want *APIError file_too_large/413", err)
	}
}

// TestJobEventsSSE - the SSE stream is parsed in the REAL server wire format (all-string field
// maps keyed by "event"); onState fires per event, and the terminal completed event yields the
// one-time download token. Regression: the CLI previously expected a typed {status,pages} shape
// the server never emits, so it dropped the token and mis-parsed progress.
func TestJobEventsSSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range []string{
			"event: page\ndata: {\"event\":\"page\",\"page\":\"1\",\"total\":\"3\"}",
			"event: page\ndata: {\"event\":\"page\",\"page\":\"3\",\"total\":\"3\"}",
			"event: completed\ndata: {\"event\":\"completed\",\"result\":\"/v1/jobs/j/result\",\"token\":\"tok-1\",\"pages\":\"3\"}",
		} {
			_, _ = io.WriteString(w, e+"\n\n")
		}
	}))
	defer srv.Close()

	var lastProgress JobState
	st, err := New(srv.URL+"/v1", "k").JobEvents(context.Background(), "j", func(s JobState) {
		if s.Status == "processing" {
			lastProgress = s
		}
	})
	if err != nil || st.Status != "completed" {
		t.Fatalf("terminal = %+v, %v, want completed", st, err)
	}
	if st.Token != "tok-1" {
		t.Fatalf("token not captured from completed event: %+v", st)
	}
	if lastProgress.PagesDone != 3 || lastProgress.Pages != 3 {
		t.Fatalf("progress not parsed: %+v", lastProgress)
	}
}

// TestJobEventsPhase - phase markers ({"event":"phase","phase":"…"}) surface the phase name as the
// status (warming/queued/processing). Regression for the mis-parse that set Status to the literal
// "phase".
func TestJobEventsPhase(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range []string{
			"event: phase\ndata: {\"event\":\"phase\",\"phase\":\"warming\"}",
			"event: phase\ndata: {\"event\":\"phase\",\"phase\":\"processing\"}",
			"event: completed\ndata: {\"event\":\"completed\",\"token\":\"tok-p\",\"pages\":\"1\"}",
		} {
			_, _ = io.WriteString(w, e+"\n\n")
		}
	}))
	defer srv.Close()
	var states []string
	st, err := New(srv.URL+"/v1", "k").JobEvents(context.Background(), "j", func(s JobState) {
		states = append(states, s.Status)
	})
	if err != nil || st.Token != "tok-p" {
		t.Fatalf("terminal = %+v, %v, want completed with token", st, err)
	}
	if got := strings.Join(states, ","); got != "warming,processing,completed" {
		t.Fatalf("phase states = %q, want warming,processing,completed", got)
	}
}

// TestJobEventsSkipsTokenlessCompleted - a token-less 'completed' event (which may precede the
// authoritative one) must NOT terminate JobEvents; it keeps scanning to the completed event that
// actually carries the download token.
func TestJobEventsSkipsTokenlessCompleted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range []string{
			"event: completed\ndata: {\"event\":\"completed\"}", // token-less marker
			"event: completed\ndata: {\"event\":\"completed\",\"token\":\"tok-2\",\"result\":\"/v1/jobs/j/result\",\"pages\":\"1\"}",
		} {
			_, _ = io.WriteString(w, e+"\n\n")
		}
	}))
	defer srv.Close()
	st, err := New(srv.URL+"/v1", "k").JobEvents(context.Background(), "j", nil)
	if err != nil || st.Token != "tok-2" {
		t.Fatalf("terminal = %+v, %v - want Token tok-2 (a token-less completed must not terminate)", st, err)
	}
}

// TestJobEventsReconnects - a dropped stream is resumed with Last-Event-ID (long-lived streams
// get dropped, so long jobs REQUIRE reconnection); the terminal event arrives on the second
// connection. Regression: the client used to give up (and lose the one-time token) on the
// first stream drop.
func TestJobEventsReconnects(t *testing.T) {
	var conns int
	var resumeID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conns++
		w.Header().Set("Content-Type", "text/event-stream")
		if conns == 1 {
			// First connection: one progress event with an id, then the stream drops.
			_, _ = io.WriteString(w, "id: ev-7\nevent: page\ndata: {\"event\":\"page\",\"page\":\"1\",\"total\":\"2\"}\n\n")
			return
		}
		resumeID = r.Header.Get("Last-Event-ID")
		_, _ = io.WriteString(w, "id: ev-8\nevent: completed\ndata: {\"event\":\"completed\",\"token\":\"tok-r\",\"pages\":\"2\"}\n\n")
	}))
	defer srv.Close()

	st, err := New(srv.URL+"/v1", "k").JobEvents(context.Background(), "j", nil)
	if err != nil || st.Token != "tok-r" {
		t.Fatalf("reconnect terminal = %+v, %v - want token tok-r", st, err)
	}
	if conns != 2 || resumeID != "ev-7" {
		t.Fatalf("conns=%d Last-Event-ID=%q, want 2 + ev-7", conns, resumeID)
	}
}

// fastSSERetries shrinks the reconnect backoff for tests that exercise the retry loop.
func fastSSERetries(t *testing.T) {
	t.Helper()
	old := sseRetryBase
	sseRetryBase = time.Millisecond
	t.Cleanup(func() { sseRetryBase = old })
}

// TestJobEventsBoundedOnReplayedState - a server that replays the same non-terminal event on
// every (re)connect must NOT reset the retry budget: JobEvents returns an error after a bounded
// number of reconnects instead of spinning forever (which used to wedge `watch`'s Ctrl-C drain).
func TestJobEventsBoundedOnReplayedState(t *testing.T) {
	fastSSERetries(t)
	var conns int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conns++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"event\":\"phase\",\"phase\":\"queued\"}\n\n") // same state every time
	}))
	defer srv.Close()

	done := make(chan error, 1)
	go func() {
		_, err := New(srv.URL+"/v1", "k").JobEvents(context.Background(), "j", nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("want an error after bounded reconnects")
		}
		if conns > sseMaxRetries+2 {
			t.Fatalf("made %d connections, want a bounded number", conns)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("JobEvents did not terminate on a replayed non-terminal state")
	}
}

// TestJobEventsRetriesOn5xx - a transient envelope-less 503 (LB blip) between reconnects consumes
// retry budget instead of aborting the wait; the one-time token still arrives on the next connect.
func TestJobEventsRetriesOn5xx(t *testing.T) {
	fastSSERetries(t)
	var conns int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conns++
		if conns == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, "<html>upstream unavailable</html>")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"event\":\"completed\",\"token\":\"tok-5xx\",\"pages\":\"1\"}\n\n")
	}))
	defer srv.Close()
	st, err := New(srv.URL+"/v1", "k").JobEvents(context.Background(), "j", nil)
	if err != nil || st.Token != "tok-5xx" {
		t.Fatalf("after 503: st=%+v err=%v, want the token from the retry", st, err)
	}
}

// TestJobEventsCanceledIsTerminal - a canceled job ends the wait instead of reconnecting forever.
// Cancel arrives as an error event with code "cancelled" (not an {"event":"canceled"}), which maps
// to the terminal "canceled" status.
func TestJobEventsCanceledIsTerminal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: error\ndata: {\"event\":\"error\",\"code\":\"cancelled\"}\n\n")
	}))
	defer srv.Close()
	st, err := New(srv.URL+"/v1", "k").JobEvents(context.Background(), "j", nil)
	if err != nil || st.Status != "canceled" {
		t.Fatalf("st=%+v err=%v, want terminal canceled", st, err)
	}
}

// TestJobEventsAuthErrorNoRetry - an HTTP-level rejection (401) returns immediately as a typed
// *APIError instead of burning reconnect attempts.
func TestJobEventsAuthErrorNoRetry(t *testing.T) {
	var conns int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conns++
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"type":"authentication_error","code":"invalid_api_key"}`)
	}))
	defer srv.Close()
	_, err := New(srv.URL+"/v1", "bad").JobEvents(context.Background(), "j", nil)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != "invalid_api_key" || conns != 1 {
		t.Fatalf("err=%v conns=%d, want one *APIError invalid_api_key attempt", err, conns)
	}
}

// TestJobResultReadOnce - a 200 returns the body; a 410 (consumed) is a typed gone *APIError.
func TestJobResultReadOnce(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"validation_error","code":"missing_token"}`))
			return
		}
		calls++
		if calls == 1 {
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = io.WriteString(w, "# Result")
			return
		}
		w.WriteHeader(http.StatusGone)
		_, _ = w.Write([]byte(`{"type":"gone","code":"result_consumed"}`))
	}))
	defer srv.Close()
	c := New(srv.URL+"/v1", "k")
	body, ct, err := c.JobResult(context.Background(), "j", "markdown", "tok")
	if err != nil || string(body) != "# Result" || !strings.HasPrefix(ct, "text/markdown") {
		t.Fatalf("first read = %q %q %v", body, ct, err)
	}
	_, _, err = c.JobResult(context.Background(), "j", "markdown", "tok")
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != "result_consumed" {
		t.Fatalf("second read = %v, want result_consumed (410)", err)
	}
}

// TestJobResultSendsToken - the read-once token is sent as ?token=; omitting it 400s missing_token.
// Regression: the CLI built the URL with no token, so every async fetch failed.
func TestJobResultSendsToken(t *testing.T) {
	var gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.URL.Query().Get("token")
		if gotToken == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"validation_error","code":"missing_token"}`))
			return
		}
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = io.WriteString(w, "# Result")
	}))
	defer srv.Close()
	c := New(srv.URL+"/v1", "k")
	if body, _, err := c.JobResult(context.Background(), "j", "markdown", "tok-77"); err != nil || string(body) != "# Result" {
		t.Fatalf("with token = %q %v", body, err)
	}
	if gotToken != "tok-77" {
		t.Fatalf("server saw token %q, want tok-77", gotToken)
	}
	_, _, err := c.JobResult(context.Background(), "j", "markdown", "")
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != "missing_token" {
		t.Fatalf("no-token = %v, want missing_token (400)", err)
	}
}
