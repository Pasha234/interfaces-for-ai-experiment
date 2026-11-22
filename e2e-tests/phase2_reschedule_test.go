package e2etests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"
)

type rescheduleBookingRequest struct {
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
}

func patchBookingRaw(t *testing.T, id bookingID, rawBody []byte) (*http.Response, []byte) {
	t.Helper()
	return doRequest(t, http.MethodPatch, "/bookings/"+url.PathEscape(string(id)), rawBody)
}

func patchBooking(t *testing.T, id bookingID, payload rescheduleBookingRequest) (*http.Response, []byte) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal rescheduleBookingRequest: %v", err)
	}
	return patchBookingRaw(t, id, data)
}

func mustRescheduleBooking(t *testing.T, id bookingID, start, end time.Time) bookingResponse {
	t.Helper()
	resp, body := patchBooking(t, id, rescheduleBookingRequest{
		StartTime: start.Format(time.RFC3339),
		EndTime:   end.Format(time.RFC3339),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 OK, got %d; body=%s", resp.StatusCode, string(body))
	}
	return decodeBooking(t, body)
}

func TestRescheduleBooking_Success(t *testing.T) {
	base := futureBaseTime()

	t.Run("move_to_free_slot", func(t *testing.T) {
		roomID := uniqueRoomID(t, "resched-free")
		origStart := base.Add(1 * time.Hour)
		origEnd := base.Add(2 * time.Hour)
		created := mustCreateBooking(t, roomID, "user-alice", origStart, origEnd)

		newStart := base.Add(4 * time.Hour)
		newEnd := base.Add(5 * time.Hour)

		updated := mustRescheduleBooking(t, created.ID, newStart, newEnd)

		// Booking identity and metadata must remain unchanged
		if updated.ID != created.ID {
			t.Fatalf("expected id=%s, got %s", created.ID, updated.ID)
		}
		assertBookingMatches(t, updated, roomID, "user-alice", newStart, newEnd)

		// Verify persistence via GET /bookings
		resp, body := getBookings(t, roomID)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200 OK, got %d; body=%s", resp.StatusCode, string(body))
		}
		list := decodeBookingsArray(t, body)
		if len(list) != 1 || list[0].ID != created.ID {
			t.Fatalf("expected 1 booking (%s) in room %s, got %+v", created.ID, roomID, list)
		}
		assertBookingMatches(t, list[0], roomID, "user-alice", newStart, newEnd)
	})

	t.Run("shift_overlapping_with_itself", func(t *testing.T) {
		// A booking must be allowed to move to an interval that overlaps with its OWN current slot.
		roomID := uniqueRoomID(t, "resched-self-overlap")
		origStart := base.Add(2 * time.Hour)
		origEnd := base.Add(4 * time.Hour)
		created := mustCreateBooking(t, roomID, "user-bob", origStart, origEnd)

		// Shift 30 minutes forward: [2.5h, 4.5h) overlaps with previous [2h, 4h)
		newStart := base.Add(2*time.Hour + 30*time.Minute)
		newEnd := base.Add(4*time.Hour + 30*time.Minute)

		updated := mustRescheduleBooking(t, created.ID, newStart, newEnd)
		if updated.ID != created.ID {
			t.Fatalf("expected id=%s, got %s", created.ID, updated.ID)
		}
		assertBookingMatches(t, updated, roomID, "user-bob", newStart, newEnd)
	})

	t.Run("shrink_within_itself", func(t *testing.T) {
		roomID := uniqueRoomID(t, "resched-shrink")
		origStart := base.Add(1 * time.Hour)
		origEnd := base.Add(4 * time.Hour)
		created := mustCreateBooking(t, roomID, "user-carol", origStart, origEnd)

		// Shrink: [2h, 3h) entirely inside original [1h, 4h)
		newStart := base.Add(2 * time.Hour)
		newEnd := base.Add(3 * time.Hour)

		updated := mustRescheduleBooking(t, created.ID, newStart, newEnd)
		if updated.ID != created.ID {
			t.Fatalf("expected id=%s, got %s", created.ID, updated.ID)
		}
		assertBookingMatches(t, updated, roomID, "user-carol", newStart, newEnd)
	})

	t.Run("expand_around_itself", func(t *testing.T) {
		roomID := uniqueRoomID(t, "resched-expand")
		origStart := base.Add(2 * time.Hour)
		origEnd := base.Add(3 * time.Hour)
		created := mustCreateBooking(t, roomID, "user-erin", origStart, origEnd)

		// Expand: [1h, 4h) strictly encloses original [2h, 3h)
		newStart := base.Add(1 * time.Hour)
		newEnd := base.Add(4 * time.Hour)

		updated := mustRescheduleBooking(t, created.ID, newStart, newEnd)
		if updated.ID != created.ID {
			t.Fatalf("expected id=%s, got %s", created.ID, updated.ID)
		}
		assertBookingMatches(t, updated, roomID, "user-erin", newStart, newEnd)

		resp, body := getBookings(t, roomID)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200 OK, got %d; body=%s", resp.StatusCode, string(body))
		}
		list := decodeBookingsArray(t, body)
		if len(list) != 1 || list[0].ID != created.ID {
			t.Fatalf("expected 1 booking (%s) in room %s, got %+v", created.ID, roomID, list)
		}
		assertBookingMatches(t, list[0], roomID, "user-erin", newStart, newEnd)
	})

	t.Run("noop_same_interval", func(t *testing.T) {
		roomID := uniqueRoomID(t, "resched-noop")
		origStart := base.Add(1 * time.Hour)
		origEnd := base.Add(2 * time.Hour)
		created := mustCreateBooking(t, roomID, "user-frank", origStart, origEnd)

		updated := mustRescheduleBooking(t, created.ID, origStart, origEnd)
		if updated.ID != created.ID {
			t.Fatalf("expected id=%s, got %s", created.ID, updated.ID)
		}
		assertBookingMatches(t, updated, roomID, "user-frank", origStart, origEnd)
	})

	t.Run("timezone_offset_format", func(t *testing.T) {
		roomID := uniqueRoomID(t, "resched-tz")
		origStart := base.Add(1 * time.Hour)
		origEnd := base.Add(2 * time.Hour)
		created := mustCreateBooking(t, roomID, "user-dave", origStart, origEnd)

		loc := time.FixedZone("UTC+3", 3*60*60)
		newStart := base.Add(5 * time.Hour).In(loc)
		newEnd := base.Add(6 * time.Hour).In(loc)

		updated := mustRescheduleBooking(t, created.ID, newStart, newEnd)
		assertBookingMatches(t, updated, roomID, "user-dave", newStart, newEnd)
	})

	t.Run("frees_old_slot_and_occupies_new_slot", func(t *testing.T) {
		roomID := uniqueRoomID(t, "resched-free-slot")
		slot1Start := base.Add(1 * time.Hour)
		slot1End := base.Add(2 * time.Hour)
		slot2Start := base.Add(3 * time.Hour)
		slot2End := base.Add(4 * time.Hour)

		b1 := mustCreateBooking(t, roomID, "user-1", slot1Start, slot1End)

		// Reschedule b1 from slot 1 to slot 2
		mustRescheduleBooking(t, b1.ID, slot2Start, slot2End)

		// Slot 1 must now be free for user-2
		b2 := mustCreateBooking(t, roomID, "user-2", slot1Start, slot1End)
		if b2.ID == "" {
			t.Fatal("expected non-empty id for booking created in freed slot")
		}

		// Slot 2 must now be occupied by b1 -> POST on slot 2 must return 409
		conflictResp, conflictBody := postBooking(t, createBookingRequest{
			RoomID:    roomID,
			UserID:    "user-3",
			StartTime: slot2Start.Format(time.RFC3339),
			EndTime:   slot2End.Format(time.RFC3339),
		})
		if conflictResp.StatusCode != http.StatusConflict {
			t.Fatalf("expected status 409 Conflict for newly occupied slot 2, got %d; body=%s", conflictResp.StatusCode, string(conflictBody))
		}
	})
}

func TestRescheduleBooking_AdjacentAllowed(t *testing.T) {
	base := futureBaseTime()

	t.Run("adjacent_before_and_after_anchor", func(t *testing.T) {
		roomID := uniqueRoomID(t, "resched-adj")

		// Stationary anchor booking: [3h, 5h)
		anchorStart := base.Add(3 * time.Hour)
		anchorEnd := base.Add(5 * time.Hour)
		anchor := mustCreateBooking(t, roomID, "user-anchor", anchorStart, anchorEnd)

		// Movable booking initially at [8h, 9h)
		movable := mustCreateBooking(t, roomID, "user-movable", base.Add(8*time.Hour), base.Add(9*time.Hour))

		// Move directly before anchor: [1h, 3h) -> end == anchor.start
		beforeStart := base.Add(1 * time.Hour)
		beforeEnd := base.Add(3 * time.Hour)
		updatedBefore := mustRescheduleBooking(t, movable.ID, beforeStart, beforeEnd)
		if updatedBefore.ID != movable.ID {
			t.Fatalf("expected id=%s, got %s", movable.ID, updatedBefore.ID)
		}
		assertBookingMatches(t, updatedBefore, roomID, "user-movable", beforeStart, beforeEnd)

		// Move directly after anchor: [5h, 7h) -> start == anchor.end
		afterStart := base.Add(5 * time.Hour)
		afterEnd := base.Add(7 * time.Hour)
		updatedAfter := mustRescheduleBooking(t, movable.ID, afterStart, afterEnd)
		if updatedAfter.ID != movable.ID {
			t.Fatalf("expected id=%s, got %s", movable.ID, updatedAfter.ID)
		}
		assertBookingMatches(t, updatedAfter, roomID, "user-movable", afterStart, afterEnd)

		// Verify both bookings exist with expected intervals
		resp, body := getBookings(t, roomID)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200 OK, got %d; body=%s", resp.StatusCode, string(body))
		}
		byID := indexByID(decodeBookingsArray(t, body))
		if len(byID) != 2 {
			t.Fatalf("expected 2 bookings in room %s, got %d", roomID, len(byID))
		}
		assertBookingMatches(t, byID[anchor.ID], roomID, "user-anchor", anchorStart, anchorEnd)
		assertBookingMatches(t, byID[movable.ID], roomID, "user-movable", afterStart, afterEnd)
	})

	t.Run("exact_fit_between_two_bookings", func(t *testing.T) {
		roomID := uniqueRoomID(t, "resched-adj-gap")

		// Left: [1h, 2h), Right: [4h, 5h), exact gap: [2h, 4h)
		mustCreateBooking(t, roomID, "user-left", base.Add(1*time.Hour), base.Add(2*time.Hour))
		mustCreateBooking(t, roomID, "user-right", base.Add(4*time.Hour), base.Add(5*time.Hour))
		movable := mustCreateBooking(t, roomID, "user-mid", base.Add(7*time.Hour), base.Add(8*time.Hour))

		gapStart := base.Add(2 * time.Hour)
		gapEnd := base.Add(4 * time.Hour)
		updated := mustRescheduleBooking(t, movable.ID, gapStart, gapEnd)
		assertBookingMatches(t, updated, roomID, "user-mid", gapStart, gapEnd)
	})
}

func TestRescheduleBooking_ValidationErrors(t *testing.T) {
	base := futureBaseTime()
	roomID := uniqueRoomID(t, "resched-val")

	// Original booking at [1h, 2h)
	origStart := base.Add(1 * time.Hour).UTC()
	origEnd := base.Add(2 * time.Hour).UTC()
	created := mustCreateBooking(t, roomID, "user-val", origStart, origEnd)

	// Use distinct times [5h, 6h) for invalid request payloads so any accidental mutation is detectable.
	otherStartTime := base.Add(5 * time.Hour).UTC()
	otherEndTime := base.Add(6 * time.Hour).UTC()
	otherStart := otherStartTime.Format(time.RFC3339)
	otherEnd := otherEndTime.Format(time.RFC3339)

	locPlus3 := time.FixedZone("UTC+3", 3*60*60)
	endEarlierDifferentTZ := otherStartTime.Add(-1 * time.Hour).In(locPlus3).Format(time.RFC3339)
	endEqualDifferentTZ := otherStartTime.In(locPlus3).Format(time.RFC3339)

	cases := []struct {
		name string
		body []byte
	}{
		{
			name: "malformed_json",
			body: []byte(`{"start_time":`),
		},
		{
			name: "empty_json_object",
			body: []byte(`{}`),
		},
		{
			name: "empty_start_time",
			body: mustMarshalJSON(t, rescheduleBookingRequest{
				StartTime: "",
				EndTime:   otherEnd,
			}),
		},
		{
			name: "omitted_start_time",
			body: mustMarshalJSON(t, map[string]any{
				"end_time": otherEnd,
			}),
		},
		{
			name: "empty_end_time",
			body: mustMarshalJSON(t, rescheduleBookingRequest{
				StartTime: otherStart,
				EndTime:   "",
			}),
		},
		{
			name: "omitted_end_time",
			body: mustMarshalJSON(t, map[string]any{
				"start_time": otherStart,
			}),
		},
		{
			name: "invalid_start_time_format",
			body: mustMarshalJSON(t, rescheduleBookingRequest{
				StartTime: "2030-01-01 10:00:00",
				EndTime:   otherEnd,
			}),
		},
		{
			name: "invalid_end_time_format",
			body: mustMarshalJSON(t, rescheduleBookingRequest{
				StartTime: otherStart,
				EndTime:   "invalid-date",
			}),
		},
		{
			name: "end_time_before_start_time",
			body: mustMarshalJSON(t, rescheduleBookingRequest{
				StartTime: otherEnd,
				EndTime:   otherStart,
			}),
		},
		{
			name: "end_time_equals_start_time",
			body: mustMarshalJSON(t, rescheduleBookingRequest{
				StartTime: otherStart,
				EndTime:   otherStart,
			}),
		},
		{
			name: "end_time_before_start_time_different_timezones",
			body: mustMarshalJSON(t, rescheduleBookingRequest{
				StartTime: otherStart,
				EndTime:   endEarlierDifferentTZ,
			}),
		},
		{
			name: "end_time_equals_start_time_different_timezones",
			body: mustMarshalJSON(t, rescheduleBookingRequest{
				StartTime: otherStart,
				EndTime:   endEqualDifferentTZ,
			}),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := patchBookingRaw(t, created.ID, tc.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected status 400 Bad Request, got %d; body=%s", resp.StatusCode, string(body))
			}
		})
	}

	// Ensure the original booking was NOT modified or deleted after invalid PATCH attempts.
	resp, body := getBookings(t, roomID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 OK, got %d; body=%s", resp.StatusCode, string(body))
	}
	list := decodeBookingsArray(t, body)
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("expected original booking (%s) to remain untouched in room %s, got %+v", created.ID, roomID, list)
	}
	assertBookingMatches(t, list[0], roomID, "user-val", origStart, origEnd)
}

func TestRescheduleBooking_NotFound(t *testing.T) {
	base := futureBaseTime()
	newStart := base.Add(10 * time.Hour).Format(time.RFC3339)
	newEnd := base.Add(11 * time.Hour).Format(time.RFC3339)
	payload := rescheduleBookingRequest{
		StartTime: newStart,
		EndTime:   newEnd,
	}

	t.Run("non_existent_id", func(t *testing.T) {
		missingID := nonExistentBookingID(t)
		resp, body := patchBooking(t, missingID, payload)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("expected status 404 Not Found for id %q, got %d; body=%s", missingID, resp.StatusCode, string(body))
		}
	})

	t.Run("deleted_booking", func(t *testing.T) {
		roomID := uniqueRoomID(t, "resched-del")
		created := mustCreateBooking(t, roomID, "user-del", base.Add(1*time.Hour), base.Add(2*time.Hour))

		// Delete the booking -> 204
		delResp, delBody := deleteBooking(t, created.ID)
		if delResp.StatusCode != http.StatusNoContent {
			t.Fatalf("expected status 204 No Content, got %d; body=%s", delResp.StatusCode, string(delBody))
		}

		// Try to reschedule deleted booking -> 404
		resp, body := patchBooking(t, created.ID, payload)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("expected status 404 Not Found on deleted booking, got %d; body=%s", resp.StatusCode, string(body))
		}
	})
}

func TestRescheduleBooking_OverlapConflicts(t *testing.T) {
	base := futureBaseTime()
	roomID := uniqueRoomID(t, "resched-conflict")

	// Booking 1 (stationary): [base+1h, base+3h)
	b1Start := base.Add(1 * time.Hour)
	b1End := base.Add(3 * time.Hour)
	mustCreateBooking(t, roomID, "user-1", b1Start, b1End)

	// Booking 2 (to be rescheduled): [base+6h, base+8h)
	origB2Start := base.Add(6 * time.Hour)
	origB2End := base.Add(8 * time.Hour)
	b2 := mustCreateBooking(t, roomID, "user-2", origB2Start, origB2End)

	loc := time.FixedZone("UTC+3", 3*60*60)

	cases := []struct {
		name  string
		start time.Time
		end   time.Time
	}{
		{
			name:  "exact_overlap_with_booking_1",
			start: b1Start,
			end:   b1End,
		},
		{
			name:  "partial_overlap_start_before_b1_end",
			start: base.Add(2 * time.Hour),
			end:   base.Add(4 * time.Hour),
		},
		{
			name:  "partial_overlap_end_after_b1_start",
			start: base.Add(30 * time.Minute),
			end:   base.Add(1*time.Hour + 30*time.Minute),
		},
		{
			name:  "enclosing_booking_1",
			start: base.Add(30 * time.Minute),
			end:   base.Add(3*time.Hour + 30*time.Minute),
		},
		{
			name:  "contained_inside_booking_1",
			start: base.Add(1*time.Hour + 15*time.Minute),
			end:   base.Add(2 * time.Hour),
		},
		{
			name:  "overlap_with_timezone_offset",
			start: b1Start.Add(15 * time.Minute).In(loc),
			end:   b1End.Add(-15 * time.Minute).In(loc),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := patchBooking(t, b2.ID, rescheduleBookingRequest{
				StartTime: tc.start.Format(time.RFC3339),
				EndTime:   tc.end.Format(time.RFC3339),
			})
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("expected status 409 Conflict, got %d; body=%s", resp.StatusCode, string(body))
			}
		})
	}

	// CRITICAL: Ensure booking 2 was NOT modified after failed attempts
	resp, body := getBookings(t, roomID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 OK, got %d; body=%s", resp.StatusCode, string(body))
	}
	list := decodeBookingsArray(t, body)
	if len(list) != 2 {
		t.Fatalf("expected 2 bookings in room %s, got %d", roomID, len(list))
	}
	byID := indexByID(list)
	gotB2, ok := byID[b2.ID]
	if !ok {
		t.Fatalf("booking 2 (%s) not found in room %s", b2.ID, roomID)
	}
	assertBookingMatches(t, gotB2, roomID, "user-2", origB2Start, origB2End)
}

func TestRescheduleBooking_AllowedInAnotherRoom(t *testing.T) {
	base := futureBaseTime()
	roomA := uniqueRoomID(t, "resched-other-a")
	roomB := uniqueRoomID(t, "resched-other-b")

	// Target slot: [base+2h, base+4h) occupied in roomA
	targetStart := base.Add(2 * time.Hour)
	targetEnd := base.Add(4 * time.Hour)
	mustCreateBooking(t, roomA, "user-a", targetStart, targetEnd)

	// Booking in roomB: initially [base+6h, base+8h)
	bB := mustCreateBooking(t, roomB, "user-b", base.Add(6*time.Hour), base.Add(8*time.Hour))

	// Rescheduling booking in roomB to target slot must succeed (200), since roomA's conflict does not apply to roomB
	updated := mustRescheduleBooking(t, bB.ID, targetStart, targetEnd)
	if updated.ID != bB.ID {
		t.Fatalf("expected id=%s, got %s", bB.ID, updated.ID)
	}
	assertBookingMatches(t, updated, roomB, "user-b", targetStart, targetEnd)
}

func TestRescheduleBooking_ConcurrentOverlap(t *testing.T) {
	const workers = 10
	base := futureBaseTime()
	roomID := uniqueRoomID(t, "resched-concurrent")

	// Place target slot strictly outside all initial bookings [base+1h, base+workers*h+45m).
	targetStart := base.Add(time.Duration(workers+2) * time.Hour)
	targetEnd := targetStart.Add(1 * time.Hour)

	// Create multiple distinct bookings in the same room at non-overlapping slots
	type workerBooking struct {
		id    bookingID
		origS time.Time
		origE time.Time
	}
	bookings := make([]workerBooking, workers)
	for i := 0; i < workers; i++ {
		s := base.Add(time.Duration(i+1) * time.Hour)
		e := s.Add(45 * time.Minute)
		created := mustCreateBooking(t, roomID, fmt.Sprintf("concurrent-user-%d", i), s, e)
		bookings[i] = workerBooking{id: created.ID, origS: s, origE: e}
	}

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
			payload, err := json.Marshal(rescheduleBookingRequest{
				StartTime: targetStart.Format(time.RFC3339),
				EndTime:   targetEnd.Format(time.RFC3339),
			})
			if err != nil {
				results[idx] = result{err: err}
				return
			}

			<-startGate

			req, err := http.NewRequest(http.MethodPatch, baseURL()+"/bookings/"+url.PathEscape(string(bookings[idx].id)), bytes.NewReader(payload))
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

	var successID bookingID
	successCount := 0
	conflictCount := 0

	for i, res := range results {
		if res.err != nil {
			t.Fatalf("worker %d failed: %v", i, res.err)
		}
		switch res.status {
		case http.StatusOK:
			b := decodeBooking(t, res.body)
			successID = b.ID
			successCount++
		case http.StatusConflict:
			conflictCount++
		default:
			t.Fatalf("worker %d got unexpected status %d; body=%s", i, res.status, string(res.body))
		}
	}

	if successCount != 1 {
		t.Fatalf("expected exactly 1 reschedule to succeed, got %d (conflicts=%d)", successCount, conflictCount)
	}
	if conflictCount != workers-1 {
		t.Fatalf("expected %d conflicts, got %d", workers-1, conflictCount)
	}

	// Verify room state: total bookings count is still `workers`, exactly 1 booking at target slot
	resp, body := getBookings(t, roomID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 OK, got %d; body=%s", resp.StatusCode, string(body))
	}
	list := decodeBookingsArray(t, body)
	if len(list) != workers {
		t.Fatalf("expected %d bookings in room %s, got %d", workers, roomID, len(list))
	}

	byID := indexByID(list)
	for i, wb := range bookings {
		got, exists := byID[wb.id]
		if !exists {
			t.Fatalf("booking %s missing after concurrent reschedules", wb.id)
		}
		if wb.id == successID {
			assertBookingMatches(t, got, roomID, fmt.Sprintf("concurrent-user-%d", i), targetStart, targetEnd)
		} else {
			assertBookingMatches(t, got, roomID, fmt.Sprintf("concurrent-user-%d", i), wb.origS, wb.origE)
		}
	}
}

func TestRescheduleBooking_ConcurrentPatchVsPost(t *testing.T) {
	const (
		nPatch = 5
		nPost  = 5
	)
	base := futureBaseTime()
	roomID := uniqueRoomID(t, "resched-vs-post")

	targetStart := base.Add(time.Duration(nPatch+3) * time.Hour)
	targetEnd := targetStart.Add(1 * time.Hour)

	type existingBooking struct {
		id     bookingID
		userID string
		origS  time.Time
		origE  time.Time
	}
	existing := make([]existingBooking, nPatch)
	for i := 0; i < nPatch; i++ {
		s := base.Add(time.Duration(i+1) * time.Hour)
		e := s.Add(45 * time.Minute)
		uid := fmt.Sprintf("patch-user-%d", i)
		created := mustCreateBooking(t, roomID, uid, s, e)
		existing[i] = existingBooking{id: created.ID, userID: uid, origS: s, origE: e}
	}

	type opResult struct {
		kind   string
		status int
		body   []byte
		err    error
	}
	results := make([]opResult, nPatch+nPost)
	startGate := make(chan struct{})
	var wg sync.WaitGroup

	// Launch PATCH workers targeting targetStart..targetEnd
	for i := 0; i < nPatch; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			payload, err := json.Marshal(rescheduleBookingRequest{
				StartTime: targetStart.Format(time.RFC3339),
				EndTime:   targetEnd.Format(time.RFC3339),
			})
			if err != nil {
				results[idx] = opResult{kind: "PATCH", err: err}
				return
			}

			<-startGate

			req, err := http.NewRequest(http.MethodPatch, baseURL()+"/bookings/"+url.PathEscape(string(existing[idx].id)), bytes.NewReader(payload))
			if err != nil {
				results[idx] = opResult{kind: "PATCH", err: err}
				return
			}
			req.Header.Set("Content-Type", "application/json")

			resp, err := httpClient.Do(req)
			if err != nil {
				results[idx] = opResult{kind: "PATCH", err: err}
				return
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			results[idx] = opResult{kind: "PATCH", status: resp.StatusCode, body: body, err: err}
		}(i)
	}

	// Launch POST workers targeting the exact same targetStart..targetEnd
	for i := 0; i < nPost; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			resIdx := nPatch + idx
			payload, err := json.Marshal(createBookingRequest{
				RoomID:    roomID,
				UserID:    fmt.Sprintf("post-user-%d", idx),
				StartTime: targetStart.Format(time.RFC3339),
				EndTime:   targetEnd.Format(time.RFC3339),
			})
			if err != nil {
				results[resIdx] = opResult{kind: "POST", err: err}
				return
			}

			<-startGate

			req, err := http.NewRequest(http.MethodPost, baseURL()+"/bookings", bytes.NewReader(payload))
			if err != nil {
				results[resIdx] = opResult{kind: "POST", err: err}
				return
			}
			req.Header.Set("Content-Type", "application/json")

			resp, err := httpClient.Do(req)
			if err != nil {
				results[resIdx] = opResult{kind: "POST", err: err}
				return
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			results[resIdx] = opResult{kind: "POST", status: resp.StatusCode, body: body, err: err}
		}(i)
	}

	close(startGate)
	wg.Wait()

	var patchWinnerID bookingID
	var postWinner bookingResponse
	patchWins := 0
	postWins := 0
	conflicts := 0

	for i, res := range results {
		if res.err != nil {
			t.Fatalf("worker %d (%s) failed: %v", i, res.kind, res.err)
		}
		switch {
		case res.kind == "PATCH" && res.status == http.StatusOK:
			b := decodeBooking(t, res.body)
			patchWinnerID = b.ID
			patchWins++
		case res.kind == "POST" && res.status == http.StatusCreated:
			b := decodeBooking(t, res.body)
			cleanupBooking(t, b.ID)
			postWinner = b
			postWins++
		case res.status == http.StatusConflict:
			conflicts++
		default:
			t.Fatalf("worker %d (%s) got unexpected status %d; body=%s", i, res.kind, res.status, string(res.body))
		}
	}

	if patchWins+postWins != 1 {
		t.Fatalf("expected exactly 1 total winner across PATCH and POST, got patchWins=%d postWins=%d conflicts=%d", patchWins, postWins, conflicts)
	}
	if conflicts != nPatch+nPost-1 {
		t.Fatalf("expected %d conflicts, got %d", nPatch+nPost-1, conflicts)
	}

	resp, body := getBookings(t, roomID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 OK, got %d; body=%s", resp.StatusCode, string(body))
	}
	list := decodeBookingsArray(t, body)
	wantCount := nPatch + postWins
	if len(list) != wantCount {
		t.Fatalf("expected %d bookings in room %s, got %d: %+v", wantCount, roomID, len(list), list)
	}

	byID := indexByID(list)
	for _, eb := range existing {
		got, ok := byID[eb.id]
		if !ok {
			t.Fatalf("existing booking %s missing from room %s", eb.id, roomID)
		}
		if patchWins == 1 && eb.id == patchWinnerID {
			assertBookingMatches(t, got, roomID, eb.userID, targetStart, targetEnd)
		} else {
			assertBookingMatches(t, got, roomID, eb.userID, eb.origS, eb.origE)
		}
	}
	if postWins == 1 {
		got, ok := byID[postWinner.ID]
		if !ok {
			t.Fatalf("winning POST booking %s missing from room %s", postWinner.ID, roomID)
		}
		assertBookingMatches(t, got, roomID, postWinner.UserID, targetStart, targetEnd)
	}
}

func TestRescheduleBooking_ConcurrentPatchVsDelete(t *testing.T) {
	const (
		rounds       = 10
		patchWorkers = 4
	)
	base := futureBaseTime()

	for round := 0; round < rounds; round++ {
		roomID := uniqueRoomID(t, fmt.Sprintf("resched-vs-del-%d", round))
		created := mustCreateBooking(t, roomID, "user-race", base.Add(1*time.Hour), base.Add(2*time.Hour))

		type res struct {
			status int
			body   []byte
			err    error
		}
		patchResults := make([]res, patchWorkers)
		var delResult res

		startGate := make(chan struct{})
		var wg sync.WaitGroup

		// 1 DELETE worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startGate
			req, err := http.NewRequest(http.MethodDelete, baseURL()+"/bookings/"+url.PathEscape(string(created.ID)), nil)
			if err != nil {
				delResult = res{err: err}
				return
			}
			resp, err := httpClient.Do(req)
			if err != nil {
				delResult = res{err: err}
				return
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			delResult = res{status: resp.StatusCode, body: body, err: err}
		}()

		// Multiple concurrent PATCH workers on the same booking ID
		for i := 0; i < patchWorkers; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				newS := base.Add(time.Duration(idx+3) * time.Hour)
				newE := newS.Add(30 * time.Minute)
				payload, err := json.Marshal(rescheduleBookingRequest{
					StartTime: newS.Format(time.RFC3339),
					EndTime:   newE.Format(time.RFC3339),
				})
				if err != nil {
					patchResults[idx] = res{err: err}
					return
				}

				<-startGate

				req, err := http.NewRequest(http.MethodPatch, baseURL()+"/bookings/"+url.PathEscape(string(created.ID)), bytes.NewReader(payload))
				if err != nil {
					patchResults[idx] = res{err: err}
					return
				}
				req.Header.Set("Content-Type", "application/json")

				resp, err := httpClient.Do(req)
				if err != nil {
					patchResults[idx] = res{err: err}
					return
				}
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				patchResults[idx] = res{status: resp.StatusCode, body: body, err: err}
			}(i)
		}

		close(startGate)
		wg.Wait()

		if delResult.err != nil {
			t.Fatalf("round %d: DELETE failed: %v", round, delResult.err)
		}
		if delResult.status != http.StatusNoContent {
			t.Fatalf("round %d: expected DELETE 204 No Content, got %d; body=%s", round, delResult.status, string(delResult.body))
		}

		for i, pr := range patchResults {
			if pr.err != nil {
				t.Fatalf("round %d: PATCH worker %d failed: %v", round, i, pr.err)
			}
			if pr.status != http.StatusOK && pr.status != http.StatusNotFound {
				t.Fatalf("round %d: PATCH worker %d got unexpected status %d (expected 200 or 404); body=%s", round, i, pr.status, string(pr.body))
			}
		}

		// CRITICAL: Deleted booking must NEVER be resurrected by a racing PATCH.
		resp, body := getBookings(t, roomID)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("round %d: expected status 200 OK, got %d; body=%s", round, resp.StatusCode, string(body))
		}
		list := decodeBookingsArray(t, body)
		if len(list) != 0 {
			t.Fatalf("round %d: deleted booking was resurrected by concurrent PATCH: %+v", round, list)
		}

		// Subsequent DELETE must return 404.
		delAgainResp, delAgainBody := deleteBooking(t, created.ID)
		if delAgainResp.StatusCode != http.StatusNotFound {
			t.Fatalf("round %d: expected 404 on second DELETE, got %d; body=%s", round, delAgainResp.StatusCode, string(delAgainBody))
		}
	}
}

func TestRescheduleBooking_ConcurrentPatchSameBooking(t *testing.T) {
	const workers = 10
	base := futureBaseTime()
	roomID := uniqueRoomID(t, "resched-same-id")

	created := mustCreateBooking(t, roomID, "user-same", base.Add(1*time.Hour), base.Add(2*time.Hour))

	type slot struct {
		start time.Time
		end   time.Time
	}
	slots := make([]slot, workers)
	for i := 0; i < workers; i++ {
		s := base.Add(time.Duration(i+3) * time.Hour)
		slots[i] = slot{start: s, end: s.Add(45 * time.Minute)}
	}

	matchesAnySubmittedSlot := func(b bookingResponse) bool {
		ps, err1 := time.Parse(time.RFC3339, b.StartTime)
		pe, err2 := time.Parse(time.RFC3339, b.EndTime)
		if err1 != nil || err2 != nil {
			return false
		}
		for _, sl := range slots {
			if ps.Equal(sl.start) && pe.Equal(sl.end) {
				return true
			}
		}
		return false
	}

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
			payload, err := json.Marshal(rescheduleBookingRequest{
				StartTime: slots[idx].start.Format(time.RFC3339),
				EndTime:   slots[idx].end.Format(time.RFC3339),
			})
			if err != nil {
				results[idx] = result{err: err}
				return
			}

			<-startGate

			req, err := http.NewRequest(http.MethodPatch, baseURL()+"/bookings/"+url.PathEscape(string(created.ID)), bytes.NewReader(payload))
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
			results[idx] = result{status: resp.StatusCode, body: body, err: err}
		}(i)
	}

	close(startGate)
	wg.Wait()

	for i, res := range results {
		if res.err != nil {
			t.Fatalf("worker %d failed: %v", i, res.err)
		}
		if res.status != http.StatusOK {
			t.Fatalf("worker %d expected status 200 OK, got %d; body=%s", i, res.status, string(res.body))
		}
		b := decodeBooking(t, res.body)
		if b.ID != created.ID || b.RoomID != roomID || b.UserID != "user-same" {
			t.Fatalf("worker %d got corrupted booking metadata: %+v", i, b)
		}
		if !matchesAnySubmittedSlot(b) {
			t.Fatalf("worker %d got inconsistent/torn time range: start=%s end=%s", i, b.StartTime, b.EndTime)
		}
	}

	// Verify final persisted booking is intact and matches one of the submitted atomic slots.
	resp, body := getBookings(t, roomID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 OK, got %d; body=%s", resp.StatusCode, string(body))
	}
	list := decodeBookingsArray(t, body)
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("expected 1 booking (%s) in room %s, got %+v", created.ID, roomID, list)
	}
	if list[0].RoomID != roomID || list[0].UserID != "user-same" {
		t.Fatalf("final booking metadata corrupted: %+v", list[0])
	}
	if !matchesAnySubmittedSlot(list[0]) {
		t.Fatalf("final booking has inconsistent/torn time range: start=%s end=%s", list[0].StartTime, list[0].EndTime)
	}
}
