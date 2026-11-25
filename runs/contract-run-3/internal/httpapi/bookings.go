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

type bookingService interface {
	Create(context.Context, domain.CreateBookingInput) (domain.Booking, error)
	List(context.Context, string) ([]domain.Booking, error)
	Cancel(context.Context, string) error
	Reschedule(context.Context, string, domain.TimeRange) (domain.Booking, error)
}

type Handler struct {
	service bookingService
}

func NewHandler(service bookingService) *Handler { return &Handler{service: service} }

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/bookings", h.bookings)
	mux.HandleFunc("/bookings/", h.bookingByID)
	return mux
}

type bookingRequest struct {
	RoomID    string `json:"room_id"`
	UserID    string `json:"user_id"`
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
}

type rescheduleRequest struct {
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

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request bookingRequest
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
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
		h.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toResponse(booking))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	roomIDs, ok := r.URL.Query()["room_id"]
	if !ok || len(roomIDs) <= 1 {
		roomID := r.URL.Query().Get("room_id")
		bookings, err := h.service.List(r.Context(), roomID)
		if err != nil {
			h.writeServiceError(w, err)
			return
		}
		response := make([]bookingResponse, 0, len(bookings))
		for _, booking := range bookings {
			response = append(response, toResponse(booking))
		}
		writeJSON(w, http.StatusOK, response)
		return
	}
	writeError(w, http.StatusBadRequest, "room_id must be specified at most once")
}

func (h *Handler) bookingByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/bookings/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodDelete:
		if err := h.service.Cancel(r.Context(), id); err != nil {
			h.writeServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPatch:
		h.reschedule(w, r, id)
	default:
		w.Header().Set("Allow", "DELETE, PATCH")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) reschedule(w http.ResponseWriter, r *http.Request, id string) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request rescheduleRequest
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
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
	booking, err := h.service.Reschedule(r.Context(), id, domain.TimeRange{Start: start, End: end})
	if err != nil {
		h.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toResponse(booking))
}

func (h *Handler) writeServiceError(w http.ResponseWriter, err error) {
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

func toResponse(booking domain.Booking) bookingResponse {
	return bookingResponse{
		ID: booking.ID, RoomID: booking.RoomID, UserID: booking.UserID,
		StartTime: booking.TimeRange.Start, EndTime: booking.TimeRange.End,
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
