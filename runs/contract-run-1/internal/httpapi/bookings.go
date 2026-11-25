package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"booking-service/internal/domain"
)

type BookingService interface {
	Create(context.Context, domain.CreateBookingInput) (domain.Booking, error)
	List(context.Context, string) ([]domain.Booking, error)
	Cancel(context.Context, string) error
}

type Handler struct {
	service BookingService
}

func NewHandler(service BookingService) http.Handler {
	h := &Handler{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("/bookings", h.bookings)
	mux.HandleFunc("/bookings/", h.booking)
	return mux
}

type bookingRequest struct {
	RoomID    string `json:"room_id"`
	UserID    string `json:"user_id"`
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
}

type bookingResponse struct {
	ID        string    `json:"id"`
	RoomID    string    `json:"room_id"`
	UserID    string    `json:"user_id"`
	StartTime time.Time `json:"start_time"`
	EndTime   time.Time `json:"end_time"`
}

func (h *Handler) bookings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.create(w, r)
	case http.MethodGet:
		h.list(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) booking(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", "DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/bookings/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	if err := h.service.Cancel(r.Context(), id); err != nil {
		if errors.Is(err, domain.ErrBookingNotFound) {
			http.NotFound(w, r)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request bookingRequest
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "request body must contain one JSON object")
		return
	}
	start, startErr := time.Parse(time.RFC3339, request.StartTime)
	end, endErr := time.Parse(time.RFC3339, request.EndTime)
	if startErr != nil || endErr != nil || request.RoomID == "" || request.UserID == "" || !start.Before(end) {
		writeError(w, http.StatusBadRequest, "room_id, user_id, and a valid start_time/end_time interval are required")
		return
	}
	booking, err := h.service.Create(r.Context(), domain.CreateBookingInput{
		RoomID:    request.RoomID,
		UserID:    request.UserID,
		TimeRange: domain.TimeRange{Start: start, End: end},
	})
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrBookingOverlap):
			writeError(w, http.StatusConflict, "booking time overlaps an existing booking")
		case errors.Is(err, domain.ErrInvalidInput), errors.Is(err, domain.ErrInvalidTimeRange):
			writeError(w, http.StatusBadRequest, "invalid booking data")
		default:
			writeError(w, http.StatusInternalServerError, "internal server error")
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(toResponse(booking))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	bookings, err := h.service.List(r.Context(), r.URL.Query().Get("room_id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	response := make([]bookingResponse, 0, len(bookings))
	for _, booking := range bookings {
		response = append(response, toResponse(booking))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func toResponse(booking domain.Booking) bookingResponse {
	return bookingResponse{ID: booking.ID, RoomID: booking.RoomID, UserID: booking.UserID, StartTime: booking.TimeRange.Start, EndTime: booking.TimeRange.End}
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
