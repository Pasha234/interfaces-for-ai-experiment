package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type booking struct {
	ID        string    `json:"id"`
	RoomID    string    `json:"room_id"`
	UserID    string    `json:"user_id"`
	StartTime time.Time `json:"start_time"`
	EndTime   time.Time `json:"end_time"`
}

type server struct{ db *sql.DB }

func main() {
	port := env("APP_PORT", "8080")
	db, err := sql.Open("pgx", databaseURL())
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	if err := migrate(ctx, db); err != nil {
		log.Fatalf("initialize database: %v", err)
	}

	s := &server{db: db}
	mux := http.NewServeMux()
	mux.HandleFunc("/bookings", s.bookings)
	mux.HandleFunc("/bookings/", s.bookingByID)
	addr := ":" + port
	log.Printf("booking service listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func (s *server) bookings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.createBooking(w, r)
	case http.MethodGet:
		s.listBookings(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *server) createBooking(w http.ResponseWriter, r *http.Request) {
	var b booking
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := decoder.Decode(&b); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		writeError(w, http.StatusBadRequest, "request must contain one JSON object")
		return
	} else if !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	b.RoomID = strings.TrimSpace(b.RoomID)
	b.UserID = strings.TrimSpace(b.UserID)
	if b.RoomID == "" || b.UserID == "" || b.StartTime.IsZero() || b.EndTime.IsZero() || !b.EndTime.After(b.StartTime) {
		writeError(w, http.StatusBadRequest, "room_id, user_id, start_time and end_time are required; end_time must be after start_time")
		return
	}
	id, err := newID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create booking")
		return
	}
	b.ID = id
	b.StartTime, b.EndTime = b.StartTime.UTC(), b.EndTime.UTC()

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create booking")
		return
	}
	defer tx.Rollback()
	// Serialize availability checks for the same room, including requests that
	// arrive concurrently, while allowing independent rooms to book in parallel.
	if _, err = tx.ExecContext(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, b.RoomID); err != nil {
		writeError(w, http.StatusInternalServerError, "could not create booking")
		return
	}
	var conflict bool
	err = tx.QueryRowContext(r.Context(), `
		SELECT EXISTS (
			SELECT 1 FROM bookings
			WHERE room_id = $1 AND cancelled_at IS NULL
			  AND start_time < $3 AND end_time > $2
		)`, b.RoomID, b.StartTime, b.EndTime).Scan(&conflict)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create booking")
		return
	}
	if conflict {
		writeError(w, http.StatusConflict, "room is already booked for that time")
		return
	}
	_, err = tx.ExecContext(r.Context(), `
		INSERT INTO bookings (id, room_id, user_id, start_time, end_time)
		VALUES ($1, $2, $3, $4, $5)`, b.ID, b.RoomID, b.UserID, b.StartTime, b.EndTime)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create booking")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "could not create booking")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(b)
}

func (s *server) listBookings(w http.ResponseWriter, r *http.Request) {
	roomID := strings.TrimSpace(r.URL.Query().Get("room_id"))
	query := `SELECT id, room_id, user_id, start_time, end_time FROM bookings WHERE cancelled_at IS NULL`
	args := []any{}
	if roomID != "" {
		query += ` AND room_id = $1`
		args = append(args, roomID)
	}
	query += ` ORDER BY start_time, id`
	rows, err := s.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list bookings")
		return
	}
	defer rows.Close()
	bookings := make([]booking, 0)
	for rows.Next() {
		var b booking
		if err := rows.Scan(&b.ID, &b.RoomID, &b.UserID, &b.StartTime, &b.EndTime); err != nil {
			writeError(w, http.StatusInternalServerError, "could not list bookings")
			return
		}
		bookings = append(bookings, b)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "could not list bookings")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(bookings)
}

func (s *server) bookingByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete && r.Method != http.MethodPatch {
		w.Header().Set("Allow", "DELETE, PATCH")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/bookings/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPatch {
		s.rescheduleBooking(w, r, id)
		return
	}
	result, err := s.db.ExecContext(r.Context(), `UPDATE bookings SET cancelled_at = now() WHERE id = $1 AND cancelled_at IS NULL`, id)
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
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) rescheduleBooking(w http.ResponseWriter, r *http.Request, id string) {
	var requested struct {
		StartTime time.Time `json:"start_time"`
		EndTime   time.Time `json:"end_time"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := decoder.Decode(&requested); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		writeError(w, http.StatusBadRequest, "request must contain one JSON object")
		return
	} else if !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if requested.StartTime.IsZero() || requested.EndTime.IsZero() || !requested.EndTime.After(requested.StartTime) {
		writeError(w, http.StatusBadRequest, "start_time and end_time are required; end_time must be after start_time")
		return
	}
	requested.StartTime = requested.StartTime.UTC()
	requested.EndTime = requested.EndTime.UTC()

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not reschedule booking")
		return
	}
	defer tx.Rollback()

	var b booking
	err = tx.QueryRowContext(r.Context(), `
		SELECT id, room_id, user_id, start_time, end_time
		FROM bookings WHERE id = $1 AND cancelled_at IS NULL
		FOR UPDATE`, id).Scan(&b.ID, &b.RoomID, &b.UserID, &b.StartTime, &b.EndTime)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not reschedule booking")
		return
	}

	// Use the same room lock as creation so concurrent bookings and
	// reschedules cannot both claim overlapping time slots.
	if _, err := tx.ExecContext(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, b.RoomID); err != nil {
		writeError(w, http.StatusInternalServerError, "could not reschedule booking")
		return
	}
	var conflict bool
	err = tx.QueryRowContext(r.Context(), `
		SELECT EXISTS (
			SELECT 1 FROM bookings
			WHERE room_id = $1 AND id <> $2 AND cancelled_at IS NULL
			  AND start_time < $4 AND end_time > $3
		)`, b.RoomID, b.ID, requested.StartTime, requested.EndTime).Scan(&conflict)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not reschedule booking")
		return
	}
	if conflict {
		writeError(w, http.StatusConflict, "room is already booked for that time")
		return
	}

	_, err = tx.ExecContext(r.Context(), `
		UPDATE bookings SET start_time = $2, end_time = $3
		WHERE id = $1`, b.ID, requested.StartTime, requested.EndTime)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not reschedule booking")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "could not reschedule booking")
		return
	}

	b.StartTime, b.EndTime = requested.StartTime, requested.EndTime
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(b)
}

func migrate(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS bookings (
			id TEXT PRIMARY KEY,
			room_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			start_time TIMESTAMPTZ NOT NULL,
			end_time TIMESTAMPTZ NOT NULL,
			cancelled_at TIMESTAMPTZ,
			CHECK (end_time > start_time)
		);
		CREATE INDEX IF NOT EXISTS bookings_room_start_active_idx
			ON bookings (room_id, start_time) WHERE cancelled_at IS NULL;
	`)
	return err
}

func newID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], raw[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], raw[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], raw[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], raw[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], raw[10:16])
	return string(encoded), nil
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func databaseURL() string {
	if value := os.Getenv("DATABASE_URL"); value != "" {
		return value
	}
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(env("DB_USER", "postgres"), env("DB_PASSWORD", "postgres")),
		Host:   env("DB_HOST", "localhost") + ":" + env("DB_PORT", "5432"),
		Path:   "/" + env("DB_NAME", "app"),
	}
	return u.String() + "?sslmode=disable"
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
