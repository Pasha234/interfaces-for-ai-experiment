package domain

import (
	"errors"
	"time"
)

var (
	ErrBookingNotFound  = errors.New("booking not found")
	ErrBookingOverlap   = errors.New("booking time overlap")
	ErrInvalidTimeRange = errors.New("invalid time range")
	ErrInvalidInput     = errors.New("invalid input")
)

type TimeRange struct {
	Start time.Time
	End   time.Time
}

type Booking struct {
	ID        string
	RoomID    string
	UserID    string
	TimeRange TimeRange
}

type CreateBookingInput struct {
	RoomID    string
	UserID    string
	TimeRange TimeRange
}
