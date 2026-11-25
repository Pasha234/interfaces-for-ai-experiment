package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"booking-service/internal/domain"

	"github.com/lib/pq"
)

type BookingRepository struct {
	db *sql.DB
}

func NewBookingRepository(db *sql.DB) *BookingRepository {
	return &BookingRepository{db: db}
}

func (r *BookingRepository) Create(ctx context.Context, booking domain.Booking) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO bookings (id, room_id, user_id, start_time, end_time)
		VALUES ($1, $2, $3, $4, $5)
	`, booking.ID, booking.RoomID, booking.UserID, booking.TimeRange.Start, booking.TimeRange.End)
	if err == nil {
		return nil
	}
	var pgErr *pq.Error
	if errors.As(err, &pgErr) && string(pgErr.Code) == "23P01" {
		return domain.ErrBookingOverlap
	}
	return fmt.Errorf("insert booking: %w", err)
}

func (r *BookingRepository) List(ctx context.Context, roomID string) ([]domain.Booking, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, room_id, user_id, start_time, end_time
		FROM bookings
		WHERE ($1 = '' OR room_id = $1)
		ORDER BY start_time, id
	`, roomID)
	if err != nil {
		return nil, fmt.Errorf("query bookings: %w", err)
	}
	defer rows.Close()

	bookings := make([]domain.Booking, 0)
	for rows.Next() {
		var booking domain.Booking
		if err := rows.Scan(&booking.ID, &booking.RoomID, &booking.UserID, &booking.TimeRange.Start, &booking.TimeRange.End); err != nil {
			return nil, fmt.Errorf("scan booking: %w", err)
		}
		bookings = append(bookings, booking)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate bookings: %w", err)
	}
	return bookings, nil
}

func (r *BookingRepository) Delete(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM bookings WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete booking: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read delete result: %w", err)
	}
	if rowsAffected == 0 {
		return domain.ErrBookingNotFound
	}
	return nil
}

func Migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS btree_gist`); err != nil {
		return fmt.Errorf("enable btree_gist extension: %w", err)
	}
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS bookings (
			id text PRIMARY KEY,
			room_id text NOT NULL CHECK (room_id <> ''),
			user_id text NOT NULL CHECK (user_id <> ''),
			start_time timestamptz NOT NULL,
			end_time timestamptz NOT NULL,
			CHECK (start_time < end_time),
			EXCLUDE USING gist (room_id WITH =, (tstzrange(start_time, end_time, '[)')) WITH &&)
		)`)
	if err != nil {
		return fmt.Errorf("run database migrations: %w", err)
	}
	return nil
}
