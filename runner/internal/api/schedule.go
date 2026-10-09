package api

import (
	"errors"
	"net/http"

	"relay/runner/internal/schedule"
)

// scheduleOn 503s every /v1/schedule* call when the booking store didn't open.
func (s *Server) scheduleOn(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Schedule == nil {
			writeError(w, http.StatusServiceUnavailable, "schedule not available")
			return
		}
		h(w, r)
	}
}

// writeScheduleError maps schedule.Service error kinds onto HTTP statuses.
func writeScheduleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, schedule.ErrNotFound):
		writeError(w, http.StatusNotFound, "booking or occurrence not found")
	case errors.Is(err, schedule.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, schedule.ErrOverlap):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) handleGetSchedule(w http.ResponseWriter, r *http.Request) {
	out, err := s.Schedule.Schedule()
	if err != nil {
		writeScheduleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleScheduleOccurrences(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out, err := s.Schedule.Occurrences(q.Get("from"), q.Get("to"))
	if err != nil {
		writeScheduleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateBooking(w http.ResponseWriter, r *http.Request) {
	var in schedule.BookingInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	b, err := s.Schedule.Create(in)
	if err != nil {
		writeScheduleError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, b)
}

func (s *Server) handleUpdateBooking(w http.ResponseWriter, r *http.Request) {
	var in schedule.BookingInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	b, err := s.Schedule.UpdateSeries(r.PathValue("id"), in)
	if err != nil {
		writeScheduleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) handleDeleteBooking(w http.ResponseWriter, r *http.Request) {
	if err := s.Schedule.DeleteSeries(r.PathValue("id")); err != nil {
		writeScheduleError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleUpdateOccurrence(w http.ResponseWriter, r *http.Request) {
	var d schedule.Day
	if err := decodeJSON(r, &d); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	b, err := s.Schedule.UpdateOccurrence(r.PathValue("id"), r.PathValue("date"), d)
	if err != nil {
		writeScheduleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) handleCancelOccurrence(w http.ResponseWriter, r *http.Request) {
	b, deleted, err := s.Schedule.CancelOccurrence(r.PathValue("id"), r.PathValue("date"))
	if err != nil {
		writeScheduleError(w, err)
		return
	}
	if deleted {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, b)
}
