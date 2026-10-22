package localcli

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type contextProxy struct {
	Endpoint string `json:"endpoint"`
	Token    string `json:"token"`
}
type contextManifest struct {
	Version int             `json:"version"`
	Sources []contextSource `json:"sources"`
	History []HistorySource `json:"history"`
	Proxy   contextProxy    `json:"proxy"`
}

func validateHistoryCatalog(sources []contextSource, history []HistorySource) error {
	if len(history) > 513 {
		return errors.New("Too many child histories")
	}
	seen := map[string]bool{}
	for _, s := range sources {
		seen[s.ID] = true
	}
	for _, h := range history {
		if h.ID == "" || len(h.ID) > 1024 || !utf8.ValidString(h.Title) || utf8.RuneCountInString(h.Title) > 1000 || h.Revision == "" || len(h.Revision) > 128 || seen[h.ID] {
			return errors.New("Invalid history source")
		}
		seen[h.ID] = true
	}
	if len(history) > 0 {
		if history[0].ParentID != "" {
			return errors.New("History must start with the parent conversation")
		}
		for _, h := range history[1:] {
			suffix, ok := strings.CutPrefix(h.ID, h.ParentID+"/child:")
			if h.ParentID != history[0].ID || h.Revision != history[0].Revision || !ok || suffix == "" || strings.Contains(suffix, "/") {
				return errors.New("Only children of the current conversation can supply history")
			}
		}
	}
	return nil
}

func historyInput(args map[string]any) (HistoryRead, error) {
	in := HistoryRead{SessionID: str(args["session_id"]), Limit: 20}
	if in.SessionID == "" {
		return in, errors.New("Choose an attached session")
	}
	for key, value := range args {
		if key == "session_id" {
			continue
		}
		if key != "offset" && key != "text_offset" && key != "limit" {
			return in, errors.New("Unsupported history parameter")
		}
		n, ok := value.(json.Number)
		if !ok {
			return in, errors.New("Use integer history offsets")
		}
		parsed, err := n.Int64()
		if err != nil || parsed < 0 {
			return in, errors.New("Use non-negative history offsets")
		}
		switch key {
		case "offset":
			in.Offset = parsed
		case "text_offset":
			if parsed > 8<<20 {
				return in, errors.New("Invalid text offset")
			}
			in.TextOffset = int(parsed)
		case "limit":
			if parsed < 1 || parsed > 120 {
				return in, errors.New("Use a limit between 1 and 120")
			}
			in.Limit = int(parsed)
		}
	}
	return in, nil
}

func validateHistoryPage(source HistorySource, in HistoryRead, page HistoryPage) error {
	if page.SessionID != source.ID || page.Revision != source.Revision || len(page.Messages) > min(in.Limit, 20) || page.NextTextOffset < 0 || page.NextTextOffset > 8<<20 {
		return errors.New("Invalid history response")
	}
	total := 0
	var previous int64 = -1
	for i, m := range page.Messages {
		if len(m.ID) < 1 || len(m.ID) > 1024 || m.Ordinal < in.Offset || m.Ordinal <= previous || (m.Role != "user" && m.Role != "assistant" && m.Role != "system" && m.Role != "tool") || !utf8.ValidString(m.Text) {
			return errors.New("Invalid history message")
		}
		if m.TextOffset < 0 || m.TextOffset > 8<<20 || (i == 0 && m.TextOffset != in.TextOffset) || (i > 0 && m.TextOffset != 0) {
			return errors.New("Invalid history text offset")
		}
		total += len(m.Text)
		previous = m.Ordinal
		if !m.Complete && i != len(page.Messages)-1 {
			return errors.New("Incomplete history message must end the page")
		}
	}
	if total > 48<<10 {
		return errors.New("History page exceeds its byte budget")
	}
	if page.NextOffset != nil {
		if len(page.Messages) == 0 || *page.NextOffset < previous || *page.NextOffset < in.Offset {
			return errors.New("Invalid history continuation")
		}
		last := page.Messages[len(page.Messages)-1]
		if last.Complete {
			if *page.NextOffset <= previous || page.NextTextOffset != 0 {
				return errors.New("Invalid next message")
			}
		} else if *page.NextOffset != previous || page.NextTextOffset != last.TextOffset+len(last.Text) || (*page.NextOffset == in.Offset && page.NextTextOffset <= in.TextOffset) {
			return errors.New("Invalid next text segment")
		}
	} else if page.NextTextOffset != 0 || len(page.Messages) > 0 && !page.Messages[len(page.Messages)-1].Complete {
		return errors.New("Missing history continuation")
	}
	return nil
}

func startContextProxy(history []HistorySource, reader HistoryReader) (contextProxy, *http.Server, error) {
	if reader == nil {
		return contextProxy{}, nil, errors.New("History reader is unavailable")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return contextProxy{}, nil, err
	}
	token := make([]byte, 32)
	if _, err = rand.Read(token); err != nil {
		listener.Close()
		return contextProxy{}, nil, err
	}
	p := contextProxy{Endpoint: "http://" + listener.Addr().String() + "/read-session", Token: base64.RawURLEncoding.EncodeToString(token)}
	slots := make(chan struct{}, 2)
	server := &http.Server{ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 4096}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != "POST" || r.URL.Path != "/read-session" || r.URL.RawQuery != "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+p.Token)) != 1 {
			http.Error(w, "Unavailable", 404)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "Busy", 429)
			return
		}
		var in HistoryRead
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "Invalid read", 400)
			return
		}
		var source *HistorySource
		for i := range history {
			if history[i].ID == in.SessionID {
				source = &history[i]
				break
			}
		}
		if source == nil || in.Offset < 0 || in.TextOffset < 0 || in.TextOffset > 8<<20 || in.Limit < 1 || in.Limit > 120 {
			http.Error(w, "Unavailable", 404)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		page, err := reader(ctx, in)
		if err != nil || validateHistoryPage(*source, in, page) != nil {
			http.Error(w, "History unavailable", 409)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(page)
	})
	go func() { _ = server.Serve(listener) }()
	return p, server, nil
}

func (p contextProxy) reader(ctx context.Context, in HistoryRead) (HistoryPage, error) {
	var out HistoryPage
	u, err := url.Parse(p.Endpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Path != "/read-session" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || len(p.Token) != 43 {
		return out, errors.New("Invalid history channel")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return out, errors.New("Invalid history channel")
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", p.Endpoint, bytes.NewReader(raw))
	if err != nil {
		return out, err
	}
	req.Header.Set("Authorization", "Bearer "+p.Token)
	req.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("History channel redirected") }}
	response, err := client.Do(req)
	if err != nil {
		return out, errors.New("History channel is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return out, errors.New("History is no longer available to this turn")
	}
	raw, err = io.ReadAll(io.LimitReader(response.Body, 512<<10+1))
	if err != nil || len(raw) > 512<<10 || !utf8.Valid(raw) {
		return out, errors.New("Invalid history response")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&out) != nil || decoder.Decode(new(any)) != io.EOF {
		return out, errors.New("Invalid history response")
	}
	return out, nil
}
