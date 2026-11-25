package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"

	"booking-service/internal/domain"
)

type BookingService struct {
	repository domain.BookingRepository
}

func NewBookingService(repository domain.BookingRepository) *BookingService {
	return &BookingService{repository: repository}
}

func (s *BookingService) Create(ctx context.Context, input domain.CreateBookingInput) (domain.Booking, error) {
	input.RoomID = strings.TrimSpace(input.RoomID)
	input.UserID = strings.TrimSpace(input.UserID)
	if input.RoomID == "" || input.UserID == "" {
		return domain.Booking{}, domain.ErrInvalidInput
	}
	if input.TimeRange.Start.IsZero() || input.TimeRange.End.IsZero() || !input.TimeRange.Start.Before(input.TimeRange.End) {
		return domain.Booking{}, domain.ErrInvalidTimeRange
	}
	id, err := newID()
	if err != nil {
		return domain.Booking{}, err
	}
	booking := domain.Booking{ID: id, RoomID: input.RoomID, UserID: input.UserID, TimeRange: input.TimeRange}
	if err := s.repository.Create(ctx, booking); err != nil {
		return domain.Booking{}, err
	}
	return booking, nil
}

func (s *BookingService) List(ctx context.Context, roomID string) ([]domain.Booking, error) {
	return s.repository.List(ctx, strings.TrimSpace(roomID))
}

func (s *BookingService) Cancel(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return domain.ErrBookingNotFound
	}
	return s.repository.Delete(ctx, id)
}

func (s *BookingService) Reschedule(ctx context.Context, id string, newRange domain.TimeRange) (domain.Booking, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return domain.Booking{}, domain.ErrBookingNotFound
	}
	if newRange.Start.IsZero() || newRange.End.IsZero() || !newRange.Start.Before(newRange.End) {
		return domain.Booking{}, domain.ErrInvalidTimeRange
	}
	return s.repository.UpdateTimeRange(ctx, id, newRange)
}

func newID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	// UUID v4: the ID is opaque to clients, but using the standard UUID layout
	// makes it easy to inspect and index in PostgreSQL.
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], id[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], id[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], id[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], id[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], id[10:16])
	return string(encoded), nil
}

var _ domain.BookingService = (*BookingService)(nil)
