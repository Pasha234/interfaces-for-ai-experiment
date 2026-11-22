package domain

import (
	"context"
)

type BookingRepository interface {
	Create(ctx context.Context, booking Booking) error
	List(ctx context.Context, roomID string) ([]Booking, error)
	Delete(ctx context.Context, id string) error
}

type BookingService interface {
	Create(ctx context.Context, input CreateBookingInput) (Booking, error)
	List(ctx context.Context, roomID string) ([]Booking, error)
	Cancel(ctx context.Context, id string) error
}
