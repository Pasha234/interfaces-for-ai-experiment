package postgres

import (
	"context"
	"database/sql"
	"errors"

	"booking-service/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

type BookingRepository struct {
	db *sql.DB
}

func NewBookingRepository(db *sql.DB) *BookingRepository {
	return &BookingRepository{db: db}
}

func (r *BookingRepository) Create(ctx context.Context, booking domain.Booking) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Serialize writes for a room so the overlap check stays safe under concurrency.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, booking.RoomID); err != nil {
		return err
	}
	var overlaps bool
	err = tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM bookings
			WHERE room_id = $1 AND start_time < $3 AND end_time > $2
		)
	`, booking.RoomID, booking.TimeRange.Start, booking.TimeRange.End).Scan(&overlaps)
	if err != nil {
		return err
	}
	if overlaps {
		return domain.ErrBookingOverlap
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO bookings (id, room_id, user_id, start_time, end_time)
		VALUES ($1, $2, $3, $4, $5)
	`, booking.ID, booking.RoomID, booking.UserID, booking.TimeRange.Start, booking.TimeRange.End)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23P01" {
			return domain.ErrBookingOverlap
		}
		return err
	}
	return tx.Commit()
}

func (r *BookingRepository) List(ctx context.Context, roomID string) ([]domain.Booking, error) {
	query := `SELECT id, room_id, user_id, start_time, end_time FROM bookings`
	args := []any{}
	if roomID != "" {
		query += ` WHERE room_id = $1`
		args = append(args, roomID)
	}
	query += ` ORDER BY start_time, id`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	bookings := make([]domain.Booking, 0)
	for rows.Next() {
		var booking domain.Booking
		if err := rows.Scan(&booking.ID, &booking.RoomID, &booking.UserID, &booking.TimeRange.Start, &booking.TimeRange.End); err != nil {
			return nil, err
		}
		bookings = append(bookings, booking)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return bookings, nil
}

func (r *BookingRepository) Delete(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM bookings WHERE id = $1`, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return domain.ErrBookingNotFound
	}
	return nil
}

func (r *BookingRepository) UpdateTimeRange(ctx context.Context, id string, newRange domain.TimeRange) (domain.Booking, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Booking{}, err
	}
	defer tx.Rollback()

	var roomID string
	err = tx.QueryRowContext(ctx, `SELECT room_id FROM bookings WHERE id = $1 FOR UPDATE`, id).Scan(&roomID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Booking{}, domain.ErrBookingNotFound
	}
	if err != nil {
		return domain.Booking{}, err
	}

	// Use the same per-room lock as Create so overlapping writes are serialized.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, roomID); err != nil {
		return domain.Booking{}, err
	}
	var overlaps bool
	err = tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM bookings
			WHERE room_id = $1 AND id <> $2 AND start_time < $4 AND end_time > $3
		)
	`, roomID, id, newRange.Start, newRange.End).Scan(&overlaps)
	if err != nil {
		return domain.Booking{}, err
	}
	if overlaps {
		return domain.Booking{}, domain.ErrBookingOverlap
	}

	var booking domain.Booking
	err = tx.QueryRowContext(ctx, `
		UPDATE bookings SET start_time = $2, end_time = $3
		WHERE id = $1
		RETURNING id, room_id, user_id, start_time, end_time
	`, id, newRange.Start, newRange.End).Scan(
		&booking.ID, &booking.RoomID, &booking.UserID, &booking.TimeRange.Start, &booking.TimeRange.End,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Booking{}, domain.ErrBookingNotFound
	}
	if err != nil {
		return domain.Booking{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Booking{}, err
	}
	return booking, nil
}

func Migrate(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS bookings (
			id TEXT PRIMARY KEY,
			room_id TEXT NOT NULL CHECK (length(trim(room_id)) > 0),
			user_id TEXT NOT NULL CHECK (length(trim(user_id)) > 0),
			start_time TIMESTAMPTZ NOT NULL,
			end_time TIMESTAMPTZ NOT NULL,
			CHECK (end_time > start_time)
		)
	`)
	return err
}
