package bookings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Booking struct {
	ID        int64     `json:"id"`
	RoomID    string    `json:"room_id"`
	UserID    string    `json:"user_id"`
	StartTime time.Time `json:"start_time"`
	EndTime   time.Time `json:"end_time"`
}

type createRequest struct {
	RoomID    string `json:"room_id"`
	UserID    string `json:"user_id"`
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
}

type rescheduleRequest struct {
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func Migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS bookings (
			id BIGSERIAL PRIMARY KEY,
			room_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			start_time TIMESTAMPTZ NOT NULL,
			end_time TIMESTAMPTZ NOT NULL,
			active BOOLEAN NOT NULL DEFAULT TRUE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			CONSTRAINT bookings_valid_interval CHECK (end_time > start_time)
		);
	`); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS bookings_active_room_time_idx
		ON bookings (room_id, start_time, end_time) WHERE active`)
	return err
}

func (s *Store) Create(w http.ResponseWriter, r *http.Request) {
	var input createRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	input.RoomID = strings.TrimSpace(input.RoomID)
	input.UserID = strings.TrimSpace(input.UserID)
	if input.RoomID == "" || input.UserID == "" {
		writeError(w, http.StatusBadRequest, "room_id and user_id are required")
		return
	}
	start, err := time.Parse(time.RFC3339, input.StartTime)
	if err != nil {
		writeError(w, http.StatusBadRequest, "start_time must be RFC3339")
		return
	}
	end, err := time.Parse(time.RFC3339, input.EndTime)
	if err != nil || !end.After(start) {
		writeError(w, http.StatusBadRequest, "end_time must be RFC3339 and later than start_time")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create booking")
		return
	}
	defer tx.Rollback()

	// A transaction-scoped advisory lock serializes overlap checks for the same room.
	var locked int
	if err = tx.QueryRowContext(ctx, `WITH lock AS (
		SELECT pg_advisory_xact_lock(hashtextextended($1, 0))
	) SELECT 1 FROM lock`, input.RoomID).Scan(&locked); err != nil {
		writeError(w, http.StatusInternalServerError, "could not create booking")
		return
	}
	var conflict bool
	err = tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM bookings
			WHERE room_id = $1 AND active
			  AND start_time < $3 AND end_time > $2
		)`, input.RoomID, start, end).Scan(&conflict)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create booking")
		return
	}
	if conflict {
		writeError(w, http.StatusConflict, "room is already booked for this time")
		return
	}

	var booking Booking
	err = tx.QueryRowContext(ctx, `
		INSERT INTO bookings (room_id, user_id, start_time, end_time)
		VALUES ($1, $2, $3, $4)
		RETURNING id, room_id, user_id, start_time, end_time`,
		input.RoomID, input.UserID, start, end,
	).Scan(&booking.ID, &booking.RoomID, &booking.UserID, &booking.StartTime, &booking.EndTime)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create booking")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "could not create booking")
		return
	}
	writeJSON(w, http.StatusCreated, booking)
}

func (s *Store) List(w http.ResponseWriter, r *http.Request) {
	roomID := strings.TrimSpace(r.URL.Query().Get("room_id"))
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	query := `SELECT id, room_id, user_id, start_time, end_time FROM bookings WHERE active`
	args := []any{}
	if roomID != "" {
		query += ` AND room_id = $1`
		args = append(args, roomID)
	}
	query += ` ORDER BY start_time, id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list bookings")
		return
	}
	defer rows.Close()
	result := make([]Booking, 0)
	for rows.Next() {
		var booking Booking
		if err := rows.Scan(&booking.ID, &booking.RoomID, &booking.UserID, &booking.StartTime, &booking.EndTime); err != nil {
			writeError(w, http.StatusInternalServerError, "could not list bookings")
			return
		}
		result = append(result, booking)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "could not list bookings")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Store) Cancel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "booking not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	result, err := s.db.ExecContext(ctx, `UPDATE bookings SET active = FALSE WHERE id = $1 AND active`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not cancel booking")
		return
	}
	count, err := result.RowsAffected()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not cancel booking")
		return
	}
	if count == 0 {
		writeError(w, http.StatusNotFound, "booking not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Store) Reschedule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "booking not found")
		return
	}

	var input rescheduleRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	start, err := time.Parse(time.RFC3339, input.StartTime)
	if err != nil {
		writeError(w, http.StatusBadRequest, "start_time must be RFC3339")
		return
	}
	end, err := time.Parse(time.RFC3339, input.EndTime)
	if err != nil || !end.After(start) {
		writeError(w, http.StatusBadRequest, "end_time must be RFC3339 and later than start_time")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not reschedule booking")
		return
	}
	defer tx.Rollback()

	var booking Booking
	err = tx.QueryRowContext(ctx, `
		SELECT id, room_id, user_id, start_time, end_time
		FROM bookings WHERE id = $1 AND active FOR UPDATE`, id,
	).Scan(&booking.ID, &booking.RoomID, &booking.UserID, &booking.StartTime, &booking.EndTime)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "booking not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not reschedule booking")
		return
	}

	// Use the same room lock as Create so concurrent bookings cannot slip
	// between the overlap check and update.
	var locked int
	err = tx.QueryRowContext(ctx, `WITH lock AS (
		SELECT pg_advisory_xact_lock(hashtextextended($1, 0))
	) SELECT 1 FROM lock`, booking.RoomID).Scan(&locked)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not reschedule booking")
		return
	}

	var conflict bool
	err = tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM bookings
			WHERE room_id = $1 AND active AND id <> $2
			  AND start_time < $4 AND end_time > $3
		)`, booking.RoomID, id, start, end).Scan(&conflict)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not reschedule booking")
		return
	}
	if conflict {
		writeError(w, http.StatusConflict, "room is already booked for this time")
		return
	}

	err = tx.QueryRowContext(ctx, `
		UPDATE bookings SET start_time = $2, end_time = $3
		WHERE id = $1 AND active
		RETURNING id, room_id, user_id, start_time, end_time`, id, start, end,
	).Scan(&booking.ID, &booking.RoomID, &booking.UserID, &booking.StartTime, &booking.EndTime)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not reschedule booking")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "could not reschedule booking")
		return
	}
	writeJSON(w, http.StatusOK, booking)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
