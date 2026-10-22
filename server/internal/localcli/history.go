package localcli

import "context"

// HistorySource identifies a complete, pinned conversation. Its contents are
// read in bounded pages through the same context tool as selected messages.
type HistorySource struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Revision string `json:"revision"`
}
type HistoryRead struct {
	SessionID  string `json:"session_id"`
	Offset     int64  `json:"offset"`
	TextOffset int    `json:"text_offset"`
	Limit      int    `json:"limit"`
}
type HistoryMessage struct {
	ID         string `json:"id"`
	Ordinal    int64  `json:"ordinal"`
	Role       string `json:"role"`
	Text       string `json:"text"`
	TextOffset int    `json:"text_offset"`
	Complete   bool   `json:"complete"`
	Source     string `json:"source,omitempty"`
}
type HistoryPage struct {
	SessionID      string           `json:"session_id"`
	Title          string           `json:"title"`
	Revision       string           `json:"revision"`
	Messages       []HistoryMessage `json:"messages"`
	NextOffset     *int64           `json:"next_offset"`
	NextTextOffset int              `json:"next_text_offset"`
}
type HistoryReader func(context.Context, HistoryRead) (HistoryPage, error)
