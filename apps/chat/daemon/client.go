package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// Client is the typed REST client for a Daemon's admin surface — the interface
// the juice-ui plugin depends on (it never touches the store or handlers
// directly). In-process it is wired to the daemon's admin handler through an
// in-memory transport (no socket), so the REST contract is exercised exactly as
// a standalone deployment's HTTP client would exercise it.
type Client struct {
	hc     *http.Client
	base   string
	secret string
}

// NewClient returns a Client bound to d's admin handler in-process.
func NewClient(d *Daemon) *Client {
	return &Client{
		hc:     &http.Client{Transport: inProcess{d.adminMux}},
		base:   "http://chat.internal",
		secret: d.adminSecret,
	}
}

// RoomsFor returns handle's rooms with unread counts.
func (c *Client) RoomsFor(ctx context.Context, handle string) ([]Room, error) {
	var out []Room
	err := c.do(ctx, http.MethodGet, "/admin/rooms?"+q("handle", handle), nil, &out)
	return out, err
}

// Room returns a room and its recent messages for handle.
func (c *Client) Room(ctx context.Context, handle, roomID string) (Room, []Message, error) {
	var out roomView
	err := c.do(ctx, http.MethodGet, "/admin/rooms/"+url.PathEscape(roomID)+"?"+q("handle", handle), nil, &out)
	return out.Room, out.Messages, err
}

// History returns older messages before beforeSeq for scrollback.
func (c *Client) History(ctx context.Context, handle, roomID string, beforeSeq int64, limit int) ([]Message, error) {
	v := url.Values{"handle": {handle}, "before": {strconv.FormatInt(beforeSeq, 10)}, "limit": {strconv.Itoa(limit)}}
	var out struct {
		Messages []Message `json:"messages"`
	}
	err := c.do(ctx, http.MethodGet, "/admin/rooms/"+url.PathEscape(roomID)+"/history?"+v.Encode(), nil, &out)
	return out.Messages, err
}

// MarkRead advances handle's read watermark for a room to seq.
func (c *Client) MarkRead(ctx context.Context, handle, roomID string, seq int64) error {
	return c.do(ctx, http.MethodPost, "/admin/rooms/"+url.PathEscape(roomID)+"/read?"+q("handle", handle),
		map[string]int64{"seq": seq}, nil)
}

// UnreadTotal is handle's unread count across all rooms (the nav badge).
func (c *Client) UnreadTotal(ctx context.Context, handle string) (int, error) {
	var out struct {
		Count int `json:"count"`
	}
	err := c.do(ctx, http.MethodGet, "/admin/unread?"+q("handle", handle), nil, &out)
	return out.Count, err
}

// do performs one admin request, mapping non-2xx responses to typed errors.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set(adminHeader, c.secret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return mapErr(resp.StatusCode, e.Error)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// mapErr turns a status code back into a sentinel where it aids the caller.
func mapErr(status int, msg string) error {
	switch status {
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusForbidden:
		return ErrForbidden
	case http.StatusBadRequest:
		return ErrBadInput
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	return fmt.Errorf("chat daemon: %s", msg)
}

// q builds a single-key query string.
func q(k, v string) string { return url.Values{k: {v}}.Encode() }

// inProcess is an http.RoundTripper that serves requests against an in-memory
// handler, so the plugin talks REST to the daemon without opening a socket.
type inProcess struct{ h http.Handler }

func (t inProcess) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := &recorder{header: http.Header{}}
	t.h.ServeHTTP(rec, r)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	return &http.Response{
		StatusCode: rec.status,
		Status:     http.StatusText(rec.status),
		Header:     rec.header,
		Body:       io.NopCloser(bytes.NewReader(rec.body.Bytes())),
		Request:    r,
	}, nil
}

// recorder is a minimal http.ResponseWriter capturing an in-memory response.
type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header { return r.header }
func (r *recorder) WriteHeader(s int)   { r.status = s }
func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(b)
}
