package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"booking-service/internal/domain"
)

type BookingService struct {
	repository domain.BookingRepository
}

func NewBookingService(repository domain.BookingRepository) *BookingService {
	return &BookingService{repository: repository}
}

func (s *BookingService) Create(ctx context.Context, input domain.CreateBookingInput) (domain.Booking, error) {
	if input.RoomID == "" || input.UserID == "" {
		return domain.Booking{}, domain.ErrInvalidInput
	}
	if input.TimeRange.Start.IsZero() || input.TimeRange.End.IsZero() || !input.TimeRange.Start.Before(input.TimeRange.End) {
		return domain.Booking{}, domain.ErrInvalidTimeRange
	}

	id, err := newID()
	if err != nil {
		return domain.Booking{}, fmt.Errorf("generate booking id: %w", err)
	}

	booking := domain.Booking{ID: id, RoomID: input.RoomID, UserID: input.UserID, TimeRange: input.TimeRange}
	if err := s.repository.Create(ctx, booking); err != nil {
		return domain.Booking{}, err
	}
	return booking, nil
}

func (s *BookingService) List(ctx context.Context, roomID string) ([]domain.Booking, error) {
	return s.repository.List(ctx, roomID)
}

func (s *BookingService) Cancel(ctx context.Context, id string) error {
	if id == "" {
		return domain.ErrInvalidInput
	}
	return s.repository.Delete(ctx, id)
}

func newID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], value[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], value[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], value[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], value[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], value[10:16])
	return string(encoded), nil
}
