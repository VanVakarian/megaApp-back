package ingest

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"time"
)

const (
	maxBatchEvents  = 1000
	maxRequestBytes = 512 * 1024
	maxEventBytes   = 64 * 1024
	maxIDLength     = 128
)

var (
	streamPattern   = regexp.MustCompile(`^[a-z0-9._-]{1,64}$`)
	clientIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

const (
	reasonNotAnObject = "not_an_object"
	reasonBadID       = "bad_id"
	reasonBadStream   = "bad_stream"
	reasonBadTime     = "bad_at"
	reasonBadData     = "bad_data"
	reasonTooLarge    = "too_large"
)

type request struct {
	Events  []json.RawMessage `json:"events"`
	Dropped int64             `json:"dropped"`
}

type rejection struct {
	Index   int    `json:"index"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type response struct {
	Received int         `json:"received"`
	Stored   int         `json:"stored"`
	Rejected []rejection `json:"rejected"`
}

type origin struct {
	source     string
	client     string
	user       *int64
	receivedAt time.Time
}

type storedLine struct {
	ReceivedAt    string          `json:"receivedAt"`
	Source        string          `json:"source"`
	Client        string          `json:"client"`
	User          *int64          `json:"user,omitempty"`
	DroppedBefore int64           `json:"droppedBefore,omitempty"`
	ID            string          `json:"id"`
	Stream        string          `json:"stream"`
	At            int64           `json:"at"`
	Data          json.RawMessage `json:"data"`
}

func encodeBatch(events []json.RawMessage, dropped int64, from origin) (lines []byte, stored int, rejected []rejection) {
	rejected = []rejection{}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)

	for index, raw := range events {
		line, reason := parseEvent(raw)
		if reason != nil {
			rejected = append(rejected, rejection{Index: index, Code: reason.code, Message: reason.message})
			continue
		}

		line.ReceivedAt = from.receivedAt.UTC().Format(time.RFC3339Nano)
		line.Source = from.source
		line.Client = from.client
		line.User = from.user
		if stored == 0 {
			line.DroppedBefore = dropped
		}
		if err := encoder.Encode(line); err != nil {
			rejected = append(rejected, rejection{Index: index, Code: reasonBadData, Message: "event cannot be re-encoded"})
			continue
		}
		stored++
	}
	return buffer.Bytes(), stored, rejected
}

type rejectionReason struct {
	code    string
	message string
}

func parseEvent(raw json.RawMessage) (storedLine, *rejectionReason) {
	var line storedLine
	if len(raw) > maxEventBytes {
		return line, &rejectionReason{reasonTooLarge, "event is larger than 64 KiB"}
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return line, &rejectionReason{reasonNotAnObject, "event must be a JSON object"}
	}

	if err := json.Unmarshal(fields["id"], &line.ID); err != nil || line.ID == "" || len(line.ID) > maxIDLength {
		return line, &rejectionReason{reasonBadID, "id must be a string of 1-128 characters"}
	}
	if err := json.Unmarshal(fields["stream"], &line.Stream); err != nil || !streamPattern.MatchString(line.Stream) {
		return line, &rejectionReason{reasonBadStream, "stream must be 1-64 characters of a-z, 0-9, dot, dash, underscore"}
	}

	at, ok := parsePositiveInteger(fields["at"])
	if !ok {
		return line, &rejectionReason{reasonBadTime, "at must be a positive integer of Unix milliseconds"}
	}
	line.At = at

	data := bytes.TrimSpace(fields["data"])
	if len(data) == 0 || data[0] != '{' {
		return line, &rejectionReason{reasonBadData, "data must be a JSON object"}
	}
	line.Data = data
	return line, nil
}

func parsePositiveInteger(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 || raw[0] < '0' || raw[0] > '9' {
		return 0, false
	}
	value, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || value <= 0 {
		return 0, false
	}
	return value, true
}
