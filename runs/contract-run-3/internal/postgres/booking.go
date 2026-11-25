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

func (r *BookingRepository) Migrate(ctx context.Context) error {
	const schema = `
CREATE EXTENSION IF NOT EXISTS btree_gist;
CREATE TABLE IF NOT EXISTS bookings (
	id TEXT PRIMARY KEY,
	room_id TEXT NOT NULL,
	user_id TEXT NOT NULL,
	start_time TIMESTAMPTZ NOT NULL,
	end_time TIMESTAMPTZ NOT NULL,
	CHECK (start_time < end_time),
	EXCLUDE USING gist (
		room_id WITH =,
		tstzrange(start_time, end_time, '[)') WITH &&
	)
);`
	if _, err := r.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("apply database schema: %w", err)
	}
	return nil
}

func (r *BookingRepository) Create(ctx context.Context, booking domain.Booking) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO bookings (id, room_id, user_id, start_time, end_time) VALUES ($1, $2, $3, $4, $5)`,
		booking.ID, booking.RoomID, booking.UserID, booking.TimeRange.Start, booking.TimeRange.End,
	)
	if err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23P01" {
			return domain.ErrBookingOverlap
		}
		return fmt.Errorf("insert booking: %w", err)
	}
	return nil
}

func (r *BookingRepository) List(ctx context.Context, roomID string) ([]domain.Booking, error) {
	query := `SELECT id, room_id, user_id, start_time, end_time FROM bookings WHERE end_time > now()`
	args := []any{}
	if roomID != "" {
		query += ` AND room_id = $1`
		args = append(args, roomID)
	}
	query += ` ORDER BY start_time, id`

	rows, err := r.db.QueryContext(ctx, query, args...)
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
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read delete result: %w", err)
	}
	if count == 0 {
		return domain.ErrBookingNotFound
	}
	return nil
}

func (r *BookingRepository) UpdateTimeRange(ctx context.Context, id string, newRange domain.TimeRange) (domain.Booking, error) {
	var booking domain.Booking
	err := r.db.QueryRowContext(ctx,
		`UPDATE bookings SET start_time = $2, end_time = $3 WHERE id = $1
		 RETURNING id, room_id, user_id, start_time, end_time`,
		id, newRange.Start, newRange.End,
	).Scan(&booking.ID, &booking.RoomID, &booking.UserID, &booking.TimeRange.Start, &booking.TimeRange.End)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Booking{}, domain.ErrBookingNotFound
		}
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23P01" {
			return domain.Booking{}, domain.ErrBookingOverlap
		}
		return domain.Booking{}, fmt.Errorf("update booking time range: %w", err)
	}
	return booking, nil
}

var _ domain.BookingRepository = (*BookingRepository)(nil)
