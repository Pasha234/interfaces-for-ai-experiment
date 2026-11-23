package e2etests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const defaultBaseURL = "http://localhost:8080"

var (
	httpClient = &http.Client{Timeout: 10 * time.Second}
	runPrefix  = strconv.FormatInt(time.Now().UnixNano(), 36)
	roomSeq    atomic.Int64
)

func baseURL() string {
	if v := strings.TrimSpace(os.Getenv("BASE_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return defaultBaseURL
}

func uniqueRoomID(t *testing.T, label string) string {
	t.Helper()
	seq := roomSeq.Add(1)
	return fmt.Sprintf("room-%s-%s-%d", runPrefix, label, seq)
}

func futureBaseTime() time.Time {
	return time.Now().UTC().Truncate(time.Hour).Add(24 * time.Hour)
}

// bookingID accepts either a JSON string or a JSON number for "id",
// storing it as a string for use in /bookings/{id}.
type bookingID string

func (id *bookingID) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*id = ""
		return nil
	}

	var s string
	if err := json.Unmarshal(trimmed, &s); err == nil {
		*id = bookingID(strings.TrimSpace(s))
		return nil
	}

	var n json.Number
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&n); err == nil {
		val := strings.TrimSpace(n.String())
		if val == "0" {
			*id = ""
			return nil
		}
		*id = bookingID(val)
		return nil
	}

	return fmt.Errorf("id must be a string or number, got %s", string(trimmed))
}

type createBookingRequest struct {
	RoomID    string `json:"room_id"`
	UserID    string `json:"user_id"`
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
}

type bookingResponse struct {
	ID        bookingID `json:"id"`
	RoomID    string    `json:"room_id"`
	UserID    string    `json:"user_id"`
	StartTime string    `json:"start_time"`
	EndTime   string    `json:"end_time"`
}

func doRequest(t *testing.T, method, endpoint string, body []byte) (*http.Response, []byte) {
	t.Helper()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequest(method, baseURL()+endpoint, reader)
	if err != nil {
		t.Fatalf("failed to build %s %s request: %v", method, endpoint, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s request failed: %v", method, endpoint, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body for %s %s: %v", method, endpoint, err)
	}

	return resp, respBody
}

func postBookingRaw(t *testing.T, rawBody []byte) (*http.Response, []byte) {
	t.Helper()
	return doRequest(t, http.MethodPost, "/bookings", rawBody)
}

func postBooking(t *testing.T, payload createBookingRequest) (*http.Response, []byte) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal createBookingRequest: %v", err)
	}
	return postBookingRaw(t, data)
}

func getBookings(t *testing.T, roomID string) (*http.Response, []byte) {
	t.Helper()
	endpoint := "/bookings"
	if roomID != "" {
		endpoint += "?room_id=" + url.QueryEscape(roomID)
	}
	return doRequest(t, http.MethodGet, endpoint, nil)
}

func deleteBooking(t *testing.T, id bookingID) (*http.Response, []byte) {
	t.Helper()
	return doRequest(t, http.MethodDelete, "/bookings/"+url.PathEscape(string(id)), nil)
}

func cleanupBooking(t *testing.T, id bookingID) {
	t.Helper()
	if id == "" {
		return
	}
	t.Cleanup(func() {
		req, err := http.NewRequest(http.MethodDelete, baseURL()+"/bookings/"+url.PathEscape(string(id)), nil)
		if err != nil {
			return
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	})
}

func decodeBooking(t *testing.T, body []byte) bookingResponse {
	t.Helper()
	var b bookingResponse
	if err := json.Unmarshal(body, &b); err != nil {
		t.Fatalf("failed to decode booking JSON: %v; body=%s", err, string(body))
	}
	return b
}

func decodeBookingsArray(t *testing.T, body []byte) []bookingResponse {
	t.Helper()
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		t.Fatalf("expected JSON array response, got: %s", string(body))
	}
	var bookings []bookingResponse
	if err := json.Unmarshal(trimmed, &bookings); err != nil {
		t.Fatalf("failed to decode bookings JSON array: %v; body=%s", err, string(body))
	}
	return bookings
}

func assertRFC3339Equal(t *testing.T, field, got string, want time.Time) {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, got)
	if err != nil {
		t.Fatalf("expected valid RFC3339 %s, got %q: %v", field, got, err)
	}
	if !parsed.Equal(want) {
		t.Fatalf("expected %s=%s, got %s", field, want.Format(time.RFC3339), got)
	}
}

func assertBookingMatches(t *testing.T, got bookingResponse, wantRoom, wantUser string, wantStart, wantEnd time.Time) {
	t.Helper()
	if got.ID == "" {
		t.Fatalf("expected non-empty booking id, got empty")
	}
	if got.RoomID != wantRoom {
		t.Fatalf("expected room_id=%q, got %q", wantRoom, got.RoomID)
	}
	if got.UserID != wantUser {
		t.Fatalf("expected user_id=%q, got %q", wantUser, got.UserID)
	}
	assertRFC3339Equal(t, "start_time", got.StartTime, wantStart)
	assertRFC3339Equal(t, "end_time", got.EndTime, wantEnd)
}

func mustCreateBooking(t *testing.T, roomID, userID string, start, end time.Time) bookingResponse {
	t.Helper()
	resp, body := postBooking(t, createBookingRequest{
		RoomID:    roomID,
		UserID:    userID,
		StartTime: start.Format(time.RFC3339),
		EndTime:   end.Format(time.RFC3339),
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected status 201 Created, got %d; body=%s", resp.StatusCode, string(body))
	}

	created := decodeBooking(t, body)
	cleanupBooking(t, created.ID)
	assertBookingMatches(t, created, roomID, userID, start, end)
	return created
}

// nonExistentBookingID detects the server's ID format via a probe booking
// (then deletes the probe) and returns a syntactically valid non-existent ID.
// This avoids false 400 errors on servers that validate UUID or integer path parameters.
func nonExistentBookingID(t *testing.T) bookingID {
	t.Helper()
	base := futureBaseTime()
	probeRoom := uniqueRoomID(t, "id-probe")
	probe := mustCreateBooking(t, probeRoom, "user-probe", base.Add(1*time.Hour), base.Add(2*time.Hour))

	delResp, delBody := deleteBooking(t, probe.ID)
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("failed to delete probe booking %s: status %d; body=%s", probe.ID, delResp.StatusCode, string(delBody))
	}

	sample := string(probe.ID)
	if _, err := strconv.ParseInt(sample, 10, 64); err == nil {
		if sample == "999999999" {
			return bookingID("999999998")
		}
		return bookingID("999999999")
	}

	if isUUID(sample) {
		candidate := "00000000-0000-4000-8000-000000000001"
		if strings.EqualFold(sample, candidate) {
			candidate = "00000000-0000-4000-8000-000000000002"
		}
		return bookingID(candidate)
	}

	if len(sample) >= 8 && isAllHex(sample) {
		candidate := strings.Repeat("0", len(sample)-1) + "1"
		if strings.EqualFold(sample, candidate) {
			candidate = strings.Repeat("0", len(sample)-1) + "2"
		}
		return bookingID(candidate)
	}

	return bookingID(sample + "-nonexistent")
}

func isHexByte(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

func isAllHex(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isHexByte(s[i]) {
			return false
		}
	}
	return true
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < 36; i++ {
		switch i {
		case 8, 13, 18, 23:
			if s[i] != '-' {
				return false
			}
		default:
			if !isHexByte(s[i]) {
				return false
			}
		}
	}
	return true
}

func mustMarshalJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("failed to marshal JSON: %v", err)
	}
	return b
}

func indexByID(bookings []bookingResponse) map[bookingID]bookingResponse {
	m := make(map[bookingID]bookingResponse, len(bookings))
	for _, b := range bookings {
		m[b.ID] = b
	}
	return m
}
