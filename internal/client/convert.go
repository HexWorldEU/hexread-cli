package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ConvertResult is the inline (synchronous) conversion output.
type ConvertResult struct {
	Markdown string        `json:"markdown"`
	Pages    []ConvertPage `json:"pages"`
	Meta     ConvertMeta   `json:"meta"`
}

type ConvertPage struct {
	Index    int    `json:"index"`
	Markdown string `json:"markdown"`
}

type ConvertMeta struct {
	Pages       int    `json:"pages"`
	SHA256      string `json:"sha256"`
	Model       string `json:"model,omitempty"`
	RouteReason string `json:"route_reason,omitempty"`
	TimingMS    int    `json:"timing_ms"`
}

// AsyncJob is the 202 response when the server picks the async path.
type AsyncJob struct {
	JobID  string   `json:"job_id"`
	Status string   `json:"status"`
	Links  JobLinks `json:"links"`
}

type JobLinks struct {
	Self   string `json:"self"`
	Events string `json:"events"`
	Result string `json:"result"`
}

// JobState is a job's status snapshot. Token/ResultPath are populated only from the SSE
// `completed` event (not the status endpoint), hence json:"-".
type JobState struct {
	JobID      string `json:"job_id"`
	Status     string `json:"status"`
	Pages      int    `json:"pages,omitempty"`
	PagesDone  int    `json:"pages_done,omitempty"`
	ErrorCode  string `json:"error_code,omitempty"`
	Token      string `json:"-"` // one-time download token (SSE completed event)
	ResultPath string `json:"-"` // result link (SSE completed event)
}

func (s JobState) Terminal() bool { return s.Status == "completed" || s.Status == "failed" }

// ConvertOptions carries the convert request options. Fields the server doesn't know are
// simply omitted from the multipart form (empty string → not sent).
type ConvertOptions struct {
	Filename       string
	Lang           string
	Model          string // auto | mineru-2.5 | granite-docling | paddleocr-vl
	Prefer         string // sync | async
	IdempotencyKey string
}

// ConvertResponse is exactly one of Inline (200 sync) or Job (202 async).
type ConvertResponse struct {
	Inline *ConvertResult
	// InlineRaw is the 200 body verbatim. `convert --json` prints it as-is, so the sync path's JSON
	// matches the async path's and no field this client does not model is dropped by a re-marshal.
	InlineRaw []byte
	Job       *AsyncJob
}

// Convert streams `src` to POST /v1/convert as multipart/form-data (never buffering the whole file)
// and returns the inline result or the async job. A non-2xx response is a typed *APIError.
func (c *Client) Convert(ctx context.Context, src io.Reader, opts ConvertOptions) (*ConvertResponse, error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		var werr error
		defer func() { _ = pw.CloseWithError(werr) }()
		for _, f := range []struct{ k, v string }{
			{"lang", opts.Lang}, {"model", opts.Model}, {"prefer", opts.Prefer},
		} {
			if f.v != "" {
				if werr = mw.WriteField(f.k, f.v); werr != nil {
					return
				}
			}
		}
		name := opts.Filename
		if name == "" {
			name = "upload"
		}
		fw, err := mw.CreateFormFile("file", name)
		if err != nil {
			werr = err
			return
		}
		if _, err := io.Copy(fw, src); err != nil {
			werr = err
			return
		}
		werr = mw.Close()
	}()

	// If ctx is canceled while the body is streaming, a Read blocked on `src` (e.g. an idle
	// stdin) would keep the transport's write loop - and so Do - blocked forever. Closing the
	// pipe's read side unblocks the transport immediately; the copier goroutine may stay
	// parked in src.Read, which is fine for a process about to exit.
	stopCancelWatch := context.AfterFunc(ctx, func() { _ = pr.CloseWithError(context.Cause(ctx)) })
	defer stopCancelWatch()

	req, err := c.newRequest(ctx, http.MethodPost, "/convert", pr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if opts.IdempotencyKey != "" {
		req.Header.Set("Idempotency-Key", opts.IdempotencyKey)
	}
	res, err := c.Stream.Do(req) // no wall-clock cap: upload + sync conversion can take minutes
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
		// Keep the bytes so --json can echo the body unchanged.
		raw, err := readAllLimited(res.Body, maxResultBytes)
		if err != nil {
			return nil, err
		}
		var r ConvertResult
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		return &ConvertResponse{Inline: &r, InlineRaw: raw}, nil
	case http.StatusAccepted:
		var j AsyncJob
		if err := json.NewDecoder(res.Body).Decode(&j); err != nil {
			return nil, err
		}
		return &ConvertResponse{Job: &j}, nil
	default:
		return nil, errorFrom(res)
	}
}

// sseMaxRetries bounds consecutive no-progress reconnects of the job event stream. The counter
// resets whenever the job's reported state CHANGES, so a long job survives any number of stream
// drops, while a stream replaying the same event forever can't spin the client indefinitely.
const sseMaxRetries = 5

// sseRetryBase scales the reconnect backoff (attempt × base); tests shrink it.
var sseRetryBase = time.Second

// sseMaxTotalDuration is an absolute ceiling on one JobEvents call. The per-reconnect budget resets
// on state changes (so long jobs survive), so a server flapping state could reconnect forever without
// this wall-clock bound. A var so tests can shrink it.
var sseMaxTotalDuration = 2 * time.Hour

// JobEvents follows a job's Server-Sent Events until a terminal event, invoking onState per
// update, and returns the terminal state. The completed event carries the one-time download
// token (st.Token) required to fetch the result. Stream drops are survived by reconnecting
// with Last-Event-ID. A 4xx rejection is final and returned as a typed *APIError; 5xx/429
// responses are transient (they must not strand the one-time result token) and consume the
// bounded retry budget.
func (c *Client) JobEvents(ctx context.Context, jobID string, onState func(JobState)) (JobState, error) {
	// Absolute backstop against a server that fakes progress to defeat the per-reconnect budget.
	ctx, cancel := context.WithTimeout(ctx, sseMaxTotalDuration)
	defer cancel()
	var last JobState
	var lastEventID string
	retries := 0
	for {
		term, progressed, err := c.streamJobEventsOnce(ctx, jobID, &lastEventID, &last, onState)
		if term != nil {
			return *term, nil
		}
		if progressed {
			retries = 0
		}
		if err != nil {
			var ae *APIError
			if errors.As(err, &ae) && ae.Status < 500 && ae.Status != http.StatusTooManyRequests {
				return last, err
			}
		}
		if ctx.Err() != nil {
			return last, ctx.Err()
		}
		retries++
		if retries > sseMaxRetries {
			if err == nil {
				err = errors.New("the job event stream ended before the job finished")
			}
			return last, err
		}
		t := time.NewTimer(time.Duration(retries) * sseRetryBase)
		select {
		case <-ctx.Done():
			t.Stop()
			return last, ctx.Err()
		case <-t.C:
		}
	}
}

// terminalState reports whether st ends the wait: failed and canceled jobs never yield a result,
// and a completed job is terminal-for-fetch only once it carries the download token - a token-less
// completed is skipped so we keep scanning for the authoritative token-bearing event.
func terminalState(st JobState) bool {
	switch st.Status {
	case "failed", "canceled", "cancelled":
		return true
	case "completed":
		return st.Token != ""
	}
	return false
}

// streamJobEventsOnce runs a single SSE connection. It returns the terminal state when one
// arrived, whether the job's state advanced during this connection, and the transport/HTTP
// error if any.
func (c *Client) streamJobEventsOnce(ctx context.Context, jobID string, lastEventID *string, last *JobState, onState func(JobState)) (*JobState, bool, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/jobs/"+jobID+"/events", nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Accept", "text/event-stream")
	if *lastEventID != "" {
		req.Header.Set("Last-Event-ID", *lastEventID)
	}
	res, err := c.Stream.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, false, errorFrom(res)
	}
	progressed := false
	pendingID := "" // an id: line applies to the event being accumulated; commit it on dispatch
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if id, ok := strings.CutPrefix(line, "id:"); ok {
			pendingID = strings.TrimSpace(id)
			continue
		}
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue // event:/blank/keepalive-comment lines - the payload is the data: JSON
		}
		// Events are all-string field maps keyed by "event"; parse loosely (JobState has int
		// fields a strict unmarshal of string values would reject) then map by event type.
		var m map[string]string
		if json.Unmarshal([]byte(strings.TrimSpace(data)), &m) != nil {
			continue
		}
		if pendingID != "" {
			*lastEventID = pendingID // the event was delivered - safe to resume after it
			pendingID = ""
		}
		st := jobStateFromEvent(jobID, m)
		if st != *last {
			progressed = true // an identical replay (e.g. re-sent status on reconnect) is not progress
		}
		*last = st
		if onState != nil {
			onState(st)
		}
		if terminalState(st) {
			return &st, true, nil
		}
	}
	return nil, progressed, sc.Err()
}

// jobStateFromEvent maps one SSE event into a JobState: page → progress counts, phase → the
// non-terminal phase (queued/warming/processing), completed → the download token + result link,
// error → the failure code. A phase event is {"event":"phase","phase":"…"}; the phase NAME becomes
// the status (a bare "phase" would match no progress-display case).
func jobStateFromEvent(jobID string, m map[string]string) JobState {
	st := JobState{JobID: jobID}
	switch m["event"] {
	case "page":
		st.Status, st.PagesDone, st.Pages = "processing", atoiSafe(m["page"]), atoiSafe(m["total"])
	case "phase":
		st.Status = m["phase"] // queued | warming | processing
	case "completed":
		st.Status, st.Pages = "completed", atoiSafe(m["pages"])
		st.Token, st.ResultPath = m["token"], m["result"]
	case "error":
		// A canceled job arrives as an error event with code "cancelled"; map it to the terminal
		// "canceled" status so it reads as a deliberate cancel, not a retryable failure.
		if c := m["code"]; c == "cancelled" || c == "canceled" {
			st.Status = "canceled"
		} else {
			st.Status, st.ErrorCode = "failed", c
		}
	default:
		// Defensive fallback for an event that names a status directly.
		st.Status = m["status"]
		if st.Status == "" {
			st.Status = m["event"]
		}
	}
	return st
}

func atoiSafe(s string) int { n, _ := strconv.Atoi(s); return n }

// JobResult fetches the read-once result, authenticated by the one-time `token` from the SSE
// completed event. format is "markdown", "json", or "" (default markdown). It returns the raw
// body + content type; a 410 (already consumed/expired) is a typed *APIError.
func (c *Client) JobResult(ctx context.Context, jobID, format, token string) ([]byte, string, error) {
	path := "/jobs/" + jobID + "/result"
	switch format {
	case "json":
		path = "/jobs/" + jobID + "/json"
	case "markdown", "md":
		path = "/jobs/" + jobID + "/markdown"
	}
	if token != "" {
		path += "?token=" + url.QueryEscape(token)
	}
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, "", err
	}
	res, err := c.Stream.Do(req) // results can be large; no wall-clock cap
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, "", errorFrom(res)
	}
	body, err := readAllLimited(res.Body, maxResultBytes)
	return body, res.Header.Get("Content-Type"), err
}
