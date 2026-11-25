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
	if input.TimeRange.Start.IsZero() || input.TimeRange.End.IsZero() || !input.TimeRange.End.After(input.TimeRange.Start) {
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
	if newRange.Start.IsZero() || newRange.End.IsZero() || !newRange.End.After(newRange.Start) {
		return domain.Booking{}, domain.ErrInvalidTimeRange
	}
	return s.repository.UpdateTimeRange(ctx, id, newRange)
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(b[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}
