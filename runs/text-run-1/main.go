package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

type booking struct {
	ID        int64     `json:"id"`
	RoomID    string    `json:"room_id"`
	UserID    string    `json:"user_id"`
	StartTime time.Time `json:"start_time"`
	EndTime   time.Time `json:"end_time"`
}

type createBookingRequest struct {
	RoomID    string `json:"room_id"`
	UserID    string `json:"user_id"`
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
}

type server struct{ db *sql.DB }

func main() {
	db, err := sql.Open("postgres", databaseURL())
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	if err := migrate(ctx, db); err != nil {
		log.Fatalf("initialize database: %v", err)
	}

	s := &server{db: db}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /bookings", s.createBooking)
	mux.HandleFunc("GET /bookings", s.listBookings)
	mux.HandleFunc("DELETE /bookings/{id}", s.deleteBooking)

	port := strings.TrimPrefix(env("APP_PORT", "8080"), ":")
	addr := ":" + port
	log.Printf("booking service listening on %s", addr)
	log.Fatal((&http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}).ListenAndServe())
}

func databaseURL() string {
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		return dsn
	}
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(env("DB_USER", "postgres"), env("DB_PASSWORD", "postgres")),
		Host:   env("DB_HOST", "localhost") + ":" + env("DB_PORT", "5432"),
		Path:   "/" + env("DB_NAME", "app"),
	}
	return u.String() + "?sslmode=" + url.QueryEscape(env("DB_SSLMODE", "disable"))
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func migrate(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS bookings (
			id BIGSERIAL PRIMARY KEY,
			room_id TEXT NOT NULL CHECK (length(room_id) > 0),
			user_id TEXT NOT NULL CHECK (length(user_id) > 0),
			start_time TIMESTAMPTZ NOT NULL,
			end_time TIMESTAMPTZ NOT NULL,
			canceled_at TIMESTAMPTZ,
			CHECK (start_time < end_time)
		);
		CREATE INDEX IF NOT EXISTS bookings_active_room_time_idx
			ON bookings (room_id, start_time, end_time)
			WHERE canceled_at IS NULL;
	`)
	return err
}

func (s *server) createBooking(w http.ResponseWriter, r *http.Request) {
	var input createBookingRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "request body must contain one JSON object")
		return
	}
	input.RoomID = strings.TrimSpace(input.RoomID)
	input.UserID = strings.TrimSpace(input.UserID)
	start, startErr := time.Parse(time.RFC3339, input.StartTime)
	end, endErr := time.Parse(time.RFC3339, input.EndTime)
	if input.RoomID == "" || input.UserID == "" || startErr != nil || endErr != nil || !start.Before(end) {
		writeError(w, http.StatusBadRequest, "room_id and user_id are required, times must be RFC3339, and start_time must precede end_time")
		return
	}

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create booking")
		return
	}
	defer tx.Rollback()

	// Serialize writes for this room so concurrent requests cannot both pass the overlap check.
	var lockAcquired int
	if err = tx.QueryRowContext(r.Context(), `SELECT 1 FROM pg_advisory_xact_lock(hashtextextended($1, 0))`, input.RoomID).Scan(&lockAcquired); err != nil {
		writeError(w, http.StatusInternalServerError, "could not create booking")
		return
	}
	var overlaps bool
	err = tx.QueryRowContext(r.Context(), `
		SELECT EXISTS (
			SELECT 1 FROM bookings
			WHERE room_id = $1 AND canceled_at IS NULL
			  AND start_time < $3 AND end_time > $2
		)
	`, input.RoomID, start, end).Scan(&overlaps)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create booking")
		return
	}
	if overlaps {
		writeError(w, http.StatusConflict, "room is already booked for that time")
		return
	}

	var result booking
	err = tx.QueryRowContext(r.Context(), `
		INSERT INTO bookings (room_id, user_id, start_time, end_time)
		VALUES ($1, $2, $3, $4)
		RETURNING id, room_id, user_id, start_time, end_time
	`, input.RoomID, input.UserID, start, end).Scan(&result.ID, &result.RoomID, &result.UserID, &result.StartTime, &result.EndTime)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create booking")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "could not create booking")
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (s *server) listBookings(w http.ResponseWriter, r *http.Request) {
	roomID := strings.TrimSpace(r.URL.Query().Get("room_id"))
	var rows *sql.Rows
	var err error
	if roomID != "" {
		rows, err = s.db.QueryContext(r.Context(), `
			SELECT id, room_id, user_id, start_time, end_time
			FROM bookings WHERE canceled_at IS NULL AND room_id = $1
			ORDER BY start_time, id
		`, roomID)
	} else {
		rows, err = s.db.QueryContext(r.Context(), `
			SELECT id, room_id, user_id, start_time, end_time
			FROM bookings WHERE canceled_at IS NULL
			ORDER BY start_time, id
		`)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list bookings")
		return
	}
	defer rows.Close()

	items := make([]booking, 0)
	for rows.Next() {
		var item booking
		if err := rows.Scan(&item.ID, &item.RoomID, &item.UserID, &item.StartTime, &item.EndTime); err != nil {
			writeError(w, http.StatusInternalServerError, "could not list bookings")
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "could not list bookings")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *server) deleteBooking(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusNotFound, "booking not found")
		return
	}
	result, err := s.db.ExecContext(r.Context(), `
		UPDATE bookings SET canceled_at = NOW()
		WHERE id = $1 AND canceled_at IS NULL
	`, id)
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

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("write response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
