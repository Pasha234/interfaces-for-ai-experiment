package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"booking-service/internal/domain"
)

type BookingHandler struct {
	service domain.BookingService
}

func NewBookingHandler(service domain.BookingService) *BookingHandler {
	return &BookingHandler{service: service}
}

func (h *BookingHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /bookings", h.create)
	mux.HandleFunc("GET /bookings", h.list)
	mux.HandleFunc("PATCH /bookings/{id}", h.reschedule)
	mux.HandleFunc("DELETE /bookings/{id}", h.cancel)
}

type bookingRequest struct {
	RoomID    string `json:"room_id"`
	UserID    string `json:"user_id"`
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
}

type rescheduleBookingRequest struct {
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
}

type bookingResponse struct {
	ID        string `json:"id"`
	RoomID    string `json:"room_id"`
	UserID    string `json:"user_id"`
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
}

func (h *BookingHandler) create(w http.ResponseWriter, r *http.Request) {
	var request bookingRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "request body must contain one JSON object")
		return
	}
	start, err := time.Parse(time.RFC3339, request.StartTime)
	if err != nil {
		writeError(w, http.StatusBadRequest, "start_time must be RFC3339")
		return
	}
	end, err := time.Parse(time.RFC3339, request.EndTime)
	if err != nil {
		writeError(w, http.StatusBadRequest, "end_time must be RFC3339")
		return
	}
	booking, err := h.service.Create(r.Context(), domain.CreateBookingInput{
		RoomID:    request.RoomID,
		UserID:    request.UserID,
		TimeRange: domain.TimeRange{Start: start, End: end},
	})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toResponse(booking))
}

func (h *BookingHandler) list(w http.ResponseWriter, r *http.Request) {
	bookings, err := h.service.List(r.Context(), r.URL.Query().Get("room_id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	responses := make([]bookingResponse, 0, len(bookings))
	for _, booking := range bookings {
		responses = append(responses, toResponse(booking))
	}
	writeJSON(w, http.StatusOK, responses)
}

func (h *BookingHandler) cancel(w http.ResponseWriter, r *http.Request) {
	if err := h.service.Cancel(r.Context(), r.PathValue("id")); err != nil {
		writeDomainError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *BookingHandler) reschedule(w http.ResponseWriter, r *http.Request) {
	var request rescheduleBookingRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "request body must contain one JSON object")
		return
	}
	start, err := time.Parse(time.RFC3339, request.StartTime)
	if err != nil {
		writeError(w, http.StatusBadRequest, "start_time must be RFC3339")
		return
	}
	end, err := time.Parse(time.RFC3339, request.EndTime)
	if err != nil {
		writeError(w, http.StatusBadRequest, "end_time must be RFC3339")
		return
	}
	booking, err := h.service.Reschedule(r.Context(), r.PathValue("id"), domain.TimeRange{Start: start, End: end})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toResponse(booking))
}

func toResponse(booking domain.Booking) bookingResponse {
	return bookingResponse{
		ID: booking.ID, RoomID: booking.RoomID, UserID: booking.UserID,
		StartTime: booking.TimeRange.Start.Format(time.RFC3339),
		EndTime:   booking.TimeRange.End.Format(time.RFC3339),
	}
}

func writeDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidInput), errors.Is(err, domain.ErrInvalidTimeRange):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, domain.ErrBookingOverlap):
		writeError(w, http.StatusConflict, "booking time overlaps an existing booking")
	case errors.Is(err, domain.ErrBookingNotFound):
		writeError(w, http.StatusNotFound, "booking not found")
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
