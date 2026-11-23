package e2etests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestCreateBooking_Success(t *testing.T) {
	base := futureBaseTime()

	t.Run("utc_timestamps", func(t *testing.T) {
		roomID := uniqueRoomID(t, "create-utc")
		start := base.Add(1 * time.Hour)
		end := base.Add(2 * time.Hour)

		created := mustCreateBooking(t, roomID, "user-alice", start, end)
		if created.ID == "" {
			t.Fatal("expected generated booking id")
		}
	})

	t.Run("timezone_offset_timestamps", func(t *testing.T) {
		roomID := uniqueRoomID(t, "create-tz")
		loc := time.FixedZone("UTC+3", 3*60*60)
		start := base.Add(3 * time.Hour).In(loc)
		end := base.Add(4 * time.Hour).In(loc)

		created := mustCreateBooking(t, roomID, "user-bob", start, end)
		if created.ID == "" {
			t.Fatal("expected generated booking id")
		}
	})
}

func TestCreateBooking_ValidationErrors(t *testing.T) {
	base := futureBaseTime()
	startTime := base.Add(5 * time.Hour).UTC()
	endTime := base.Add(6 * time.Hour).UTC()
	validStart := startTime.Format(time.RFC3339)
	validEnd := endTime.Format(time.RFC3339)
	roomID := uniqueRoomID(t, "invalid")

	locPlus3 := time.FixedZone("UTC+3", 3*60*60)
	// Lexicographically larger timestamp string (due to +03:00 offset) that is actually earlier/equal in UTC.
	endEarlierDifferentTZ := startTime.Add(-1 * time.Hour).In(locPlus3).Format(time.RFC3339)
	endEqualDifferentTZ := startTime.In(locPlus3).Format(time.RFC3339)

	cases := []struct {
		name string
		body []byte
	}{
		{
			name: "malformed_json",
			body: []byte(`{"room_id": "r1",`),
		},
		{
			name: "empty_json_object",
			body: []byte(`{}`),
		},
		{
			name: "empty_room_id",
			body: mustMarshalJSON(t, createBookingRequest{
				RoomID:    "",
				UserID:    "user-1",
				StartTime: validStart,
				EndTime:   validEnd,
			}),
		},
		{
			name: "omitted_room_id",
			body: mustMarshalJSON(t, map[string]any{
				"user_id":    "user-1",
				"start_time": validStart,
				"end_time":   validEnd,
			}),
		},
		{
			name: "empty_user_id",
			body: mustMarshalJSON(t, createBookingRequest{
				RoomID:    roomID,
				UserID:    "",
				StartTime: validStart,
				EndTime:   validEnd,
			}),
		},
		{
			name: "omitted_user_id",
			body: mustMarshalJSON(t, map[string]any{
				"room_id":    roomID,
				"start_time": validStart,
				"end_time":   validEnd,
			}),
		},
		{
			name: "empty_start_time",
			body: mustMarshalJSON(t, createBookingRequest{
				RoomID:    roomID,
				UserID:    "user-1",
				StartTime: "",
				EndTime:   validEnd,
			}),
		},
		{
			name: "omitted_start_time",
			body: mustMarshalJSON(t, map[string]any{
				"room_id":  roomID,
				"user_id":  "user-1",
				"end_time": validEnd,
			}),
		},
		{
			name: "empty_end_time",
			body: mustMarshalJSON(t, createBookingRequest{
				RoomID:    roomID,
				UserID:    "user-1",
				StartTime: validStart,
				EndTime:   "",
			}),
		},
		{
			name: "omitted_end_time",
			body: mustMarshalJSON(t, map[string]any{
				"room_id":    roomID,
				"user_id":    "user-1",
				"start_time": validStart,
			}),
		},
		{
			name: "invalid_start_time_format",
			body: mustMarshalJSON(t, createBookingRequest{
				RoomID:    roomID,
				UserID:    "user-1",
				StartTime: "2030-01-01 10:00:00",
				EndTime:   validEnd,
			}),
		},
		{
			name: "invalid_end_time_format",
			body: mustMarshalJSON(t, createBookingRequest{
				RoomID:    roomID,
				UserID:    "user-1",
				StartTime: validStart,
				EndTime:   "not-a-date",
			}),
		},
		{
			name: "end_time_before_start_time",
			body: mustMarshalJSON(t, createBookingRequest{
				RoomID:    roomID,
				UserID:    "user-1",
				StartTime: validEnd,
				EndTime:   validStart,
			}),
		},
		{
			name: "end_time_equals_start_time",
			body: mustMarshalJSON(t, createBookingRequest{
				RoomID:    roomID,
				UserID:    "user-1",
				StartTime: validStart,
				EndTime:   validStart,
			}),
		},
		{
			name: "end_time_before_start_time_different_timezones",
			body: mustMarshalJSON(t, createBookingRequest{
				RoomID:    roomID,
				UserID:    "user-1",
				StartTime: validStart,
				EndTime:   endEarlierDifferentTZ,
			}),
		},
		{
			name: "end_time_equals_start_time_different_timezones",
			body: mustMarshalJSON(t, createBookingRequest{
				RoomID:    roomID,
				UserID:    "user-1",
				StartTime: validStart,
				EndTime:   endEqualDifferentTZ,
			}),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := postBookingRaw(t, tc.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected status 400 Bad Request, got %d; body=%s", resp.StatusCode, string(body))
			}
		})
	}

	// Ensure no invalid booking was persisted in the room.
	resp, body := getBookings(t, roomID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 OK, got %d; body=%s", resp.StatusCode, string(body))
	}
	list := decodeBookingsArray(t, body)
	if len(list) != 0 {
		t.Fatalf("expected 0 bookings in room %s after invalid requests, got %+v", roomID, list)
	}
}

func TestCreateBooking_OverlapConflicts(t *testing.T) {
	base := futureBaseTime()
	roomID := uniqueRoomID(t, "overlap")

	// Existing booking: [base+2h, base+4h)
	origStart := base.Add(2 * time.Hour)
	origEnd := base.Add(4 * time.Hour)
	existing := mustCreateBooking(t, roomID, "user-owner", origStart, origEnd)

	loc := time.FixedZone("UTC+3", 3*60*60)

	cases := []struct {
		name   string
		userID string
		start  time.Time
		end    time.Time
	}{
		{
			name:   "exact_same_interval",
			userID: "user-owner",
			start:  origStart,
			end:    origEnd,
		},
		{
			name:   "partial_overlap_starting_before",
			userID: "user-2",
			start:  base.Add(1 * time.Hour),
			end:    base.Add(3 * time.Hour),
		},
		{
			name:   "partial_overlap_ending_after",
			userID: "user-3",
			start:  base.Add(3 * time.Hour),
			end:    base.Add(5 * time.Hour),
		},
		{
			name:   "contained_inside_existing",
			userID: "user-4",
			start:  base.Add(2*time.Hour + 30*time.Minute),
			end:    base.Add(3*time.Hour + 30*time.Minute),
		},
		{
			name:   "enclosing_existing",
			userID: "user-5",
			start:  base.Add(1 * time.Hour),
			end:    base.Add(5 * time.Hour),
		},
		{
			name:   "overlap_with_timezone_offset",
			userID: "user-6",
			start:  origStart.Add(15 * time.Minute).In(loc),
			end:    origEnd.Add(-15 * time.Minute).In(loc),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := postBooking(t, createBookingRequest{
				RoomID:    roomID,
				UserID:    tc.userID,
				StartTime: tc.start.Format(time.RFC3339),
				EndTime:   tc.end.Format(time.RFC3339),
			})
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("expected status 409 Conflict, got %d; body=%s", resp.StatusCode, string(body))
			}
		})
	}

	// Ensure no conflicting booking was persisted.
	resp, body := getBookings(t, roomID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 OK, got %d; body=%s", resp.StatusCode, string(body))
	}
	list := decodeBookingsArray(t, body)
	if len(list) != 1 || list[0].ID != existing.ID {
		t.Fatalf("expected exactly 1 booking (%s) in room %s, got %+v", existing.ID, roomID, list)
	}
}

func TestCreateBooking_NonOverlappingAllowed(t *testing.T) {
	base := futureBaseTime()
	roomA := uniqueRoomID(t, "adj-a")
	roomB := uniqueRoomID(t, "adj-b")

	// Base booking in roomA: [base+3h, base+5h)
	mid := mustCreateBooking(t, roomA, "user-1", base.Add(3*time.Hour), base.Add(5*time.Hour))

	// Adjacent before in roomA: [base+1h, base+3h)
	before := mustCreateBooking(t, roomA, "user-2", base.Add(1*time.Hour), base.Add(3*time.Hour))

	// Adjacent after in roomA: [base+5h, base+7h)
	after := mustCreateBooking(t, roomA, "user-3", base.Add(5*time.Hour), base.Add(7*time.Hour))

	// Same time slot as mid, but in different roomB
	otherRoom := mustCreateBooking(t, roomB, "user-4", base.Add(3*time.Hour), base.Add(5*time.Hour))

	respA, bodyA := getBookings(t, roomA)
	if respA.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 OK for roomA, got %d; body=%s", respA.StatusCode, string(bodyA))
	}
	listA := decodeBookingsArray(t, bodyA)
	if len(listA) != 3 {
		t.Fatalf("expected 3 bookings in roomA, got %d: %+v", len(listA), listA)
	}
	idsA := indexByID(listA)
	for _, wantID := range []bookingID{mid.ID, before.ID, after.ID} {
		if _, ok := idsA[wantID]; !ok {
			t.Fatalf("expected booking %s in roomA list, got %+v", wantID, listA)
		}
	}

	respB, bodyB := getBookings(t, roomB)
	if respB.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 OK for roomB, got %d; body=%s", respB.StatusCode, string(bodyB))
	}
	listB := decodeBookingsArray(t, bodyB)
	if len(listB) != 1 || listB[0].ID != otherRoom.ID {
		t.Fatalf("expected 1 booking (%s) in roomB, got %+v", otherRoom.ID, listB)
	}
}

func TestListBookings_AllAndFilterByRoom(t *testing.T) {
	base := futureBaseTime()
	roomA := uniqueRoomID(t, "list-a")
	roomB := uniqueRoomID(t, "list-b")
	roomEmpty := uniqueRoomID(t, "list-empty")

	bA1 := mustCreateBooking(t, roomA, "user-1", base.Add(1*time.Hour), base.Add(2*time.Hour))
	bA2 := mustCreateBooking(t, roomA, "user-2", base.Add(3*time.Hour), base.Add(4*time.Hour))
	bB1 := mustCreateBooking(t, roomB, "user-3", base.Add(1*time.Hour), base.Add(2*time.Hour))

	t.Run("list_all_contains_created_bookings", func(t *testing.T) {
		resp, body := getBookings(t, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200 OK, got %d; body=%s", resp.StatusCode, string(body))
		}
		all := decodeBookingsArray(t, body)
		byID := indexByID(all)

		gotA1, ok := byID[bA1.ID]
		if !ok {
			t.Fatalf("booking %s missing from GET /bookings", bA1.ID)
		}
		assertBookingMatches(t, gotA1, roomA, "user-1", base.Add(1*time.Hour), base.Add(2*time.Hour))

		gotA2, ok := byID[bA2.ID]
		if !ok {
			t.Fatalf("booking %s missing from GET /bookings", bA2.ID)
		}
		assertBookingMatches(t, gotA2, roomA, "user-2", base.Add(3*time.Hour), base.Add(4*time.Hour))

		gotB1, ok := byID[bB1.ID]
		if !ok {
			t.Fatalf("booking %s missing from GET /bookings", bB1.ID)
		}
		assertBookingMatches(t, gotB1, roomB, "user-3", base.Add(1*time.Hour), base.Add(2*time.Hour))
	})

	t.Run("filter_by_room_id", func(t *testing.T) {
		resp, body := getBookings(t, roomA)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200 OK, got %d; body=%s", resp.StatusCode, string(body))
		}
		list := decodeBookingsArray(t, body)
		if len(list) != 2 {
			t.Fatalf("expected 2 bookings for %s, got %d: %+v", roomA, len(list), list)
		}
		byID := indexByID(list)
		if _, ok := byID[bA1.ID]; !ok {
			t.Fatalf("expected booking %s in room %s", bA1.ID, roomA)
		}
		if _, ok := byID[bA2.ID]; !ok {
			t.Fatalf("expected booking %s in room %s", bA2.ID, roomA)
		}
		if _, ok := byID[bB1.ID]; ok {
			t.Fatalf("did not expect booking %s from another room in room %s", bB1.ID, roomA)
		}
	})

	t.Run("filter_by_nonexistent_room_returns_empty_json_array", func(t *testing.T) {
		resp, body := getBookings(t, roomEmpty)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200 OK, got %d; body=%s", resp.StatusCode, string(body))
		}
		list := decodeBookingsArray(t, body)
		if len(list) != 0 {
			t.Fatalf("expected 0 bookings for %s, got %+v", roomEmpty, list)
		}
	})
}

func TestDeleteBooking_LifecycleAndRebooking(t *testing.T) {
	base := futureBaseTime()
	roomID := uniqueRoomID(t, "delete-life")
	start := base.Add(2 * time.Hour)
	end := base.Add(3 * time.Hour)
	siblingStart := base.Add(5 * time.Hour)
	siblingEnd := base.Add(6 * time.Hour)

	created := mustCreateBooking(t, roomID, "user-1", start, end)
	sibling := mustCreateBooking(t, roomID, "user-sibling", siblingStart, siblingEnd)

	// Delete existing booking -> 204
	delResp, delBody := deleteBooking(t, created.ID)
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected status 204 No Content, got %d; body=%s", delResp.StatusCode, string(delBody))
	}

	// Verify deleted booking is gone from filtered list while sibling remains intact
	roomResp, roomBody := getBookings(t, roomID)
	if roomResp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 OK, got %d; body=%s", roomResp.StatusCode, string(roomBody))
	}
	roomList := decodeBookingsArray(t, roomBody)
	if len(roomList) != 1 || roomList[0].ID != sibling.ID {
		t.Fatalf("expected only sibling booking %s to remain after deletion, got %+v", sibling.ID, roomList)
	}
	assertBookingMatches(t, roomList[0], roomID, "user-sibling", siblingStart, siblingEnd)

	// Verify deleted booking is gone from global list while sibling is present
	allResp, allBody := getBookings(t, "")
	if allResp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 OK, got %d; body=%s", allResp.StatusCode, string(allBody))
	}
	allByID := indexByID(decodeBookingsArray(t, allBody))
	if _, exists := allByID[created.ID]; exists {
		t.Fatalf("deleted booking %s still present in GET /bookings", created.ID)
	}
	if _, exists := allByID[sibling.ID]; !exists {
		t.Fatalf("sibling booking %s missing from GET /bookings after deleting another booking", sibling.ID)
	}

	// Deleting the same booking again -> 404
	secondDelResp, secondDelBody := deleteBooking(t, created.ID)
	if secondDelResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected status 404 Not Found on second delete, got %d; body=%s", secondDelResp.StatusCode, string(secondDelBody))
	}

	// Re-booking the exact same room and time slot -> 201
	rebooked := mustCreateBooking(t, roomID, "user-2", start, end)
	if rebooked.ID == "" {
		t.Fatal("expected non-empty id for rebooked slot")
	}

	// Verify both sibling and rebooked are present
	roomResp2, roomBody2 := getBookings(t, roomID)
	if roomResp2.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 OK, got %d; body=%s", roomResp2.StatusCode, string(roomBody2))
	}
	roomList2 := decodeBookingsArray(t, roomBody2)
	if len(roomList2) != 2 {
		t.Fatalf("expected 2 bookings (sibling + rebooked) in room %s, got %+v", roomID, roomList2)
	}
}

func TestDeleteBooking_NotFound(t *testing.T) {
	missingID := nonExistentBookingID(t)
	resp, body := deleteBooking(t, missingID)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected status 404 Not Found for id %q, got %d; body=%s", missingID, resp.StatusCode, string(body))
	}
}

func TestCreateBooking_ConcurrentOverlap(t *testing.T) {
	const workers = 15
	base := futureBaseTime()
	roomID := uniqueRoomID(t, "concurrent")
	start := base.Add(6 * time.Hour)
	end := base.Add(7 * time.Hour)

	type result struct {
		status int
		body   []byte
		err    error
	}

	results := make([]result, workers)
	startGate := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			payload, err := json.Marshal(createBookingRequest{
				RoomID:    roomID,
				UserID:    fmt.Sprintf("concurrent-user-%d", idx),
				StartTime: start.Format(time.RFC3339),
				EndTime:   end.Format(time.RFC3339),
			})
			if err != nil {
				results[idx] = result{err: err}
				return
			}

			<-startGate

			req, err := http.NewRequest(http.MethodPost, baseURL()+"/bookings", bytes.NewReader(payload))
			if err != nil {
				results[idx] = result{err: err}
				return
			}
			req.Header.Set("Content-Type", "application/json")

			resp, err := httpClient.Do(req)
			if err != nil {
				results[idx] = result{err: err}
				return
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			results[idx] = result{
				status: resp.StatusCode,
				body:   body,
				err:    err,
			}
		}(i)
	}

	close(startGate)
	wg.Wait()

	var createdIDs []bookingID
	conflictCount := 0

	for i, res := range results {
		if res.err != nil {
			t.Fatalf("worker %d failed: %v", i, res.err)
		}
		switch res.status {
		case http.StatusCreated:
			b := decodeBooking(t, res.body)
			cleanupBooking(t, b.ID)
			createdIDs = append(createdIDs, b.ID)
		case http.StatusConflict:
			conflictCount++
		default:
			t.Fatalf("worker %d got unexpected status %d; body=%s", i, res.status, string(res.body))
		}
	}

	if len(createdIDs) != 1 {
		t.Fatalf("expected exactly 1 booking to succeed under concurrent overlap, got %d (conflicts=%d)", len(createdIDs), conflictCount)
	}
	if conflictCount != workers-1 {
		t.Fatalf("expected %d conflicts, got %d", workers-1, conflictCount)
	}

	resp, body := getBookings(t, roomID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 OK, got %d; body=%s", resp.StatusCode, string(body))
	}
	list := decodeBookingsArray(t, body)
	if len(list) != 1 || list[0].ID != createdIDs[0] {
		t.Fatalf("expected exactly 1 persisted booking (%s) in room %s, got %+v", createdIDs[0], roomID, list)
	}
}
