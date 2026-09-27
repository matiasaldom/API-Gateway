package httpx

import "net/http"

// StatusRecorder wraps a ResponseWriter to record the response status and body size.
type StatusRecorder struct {
	http.ResponseWriter
	Status      int
	Bytes       int
	wroteHeader bool
}

// NewStatusRecorder returns a recorder whose Status defaults to 200, as net/http does.
func NewStatusRecorder(w http.ResponseWriter) *StatusRecorder {
	return &StatusRecorder{ResponseWriter: w, Status: http.StatusOK}
}

func (s *StatusRecorder) WriteHeader(code int) {
	if !s.wroteHeader && code >= 200 { // 1xx responses precede the final status
		s.Status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *StatusRecorder) Write(b []byte) (int, error) {
	s.wroteHeader = true
	n, err := s.ResponseWriter.Write(b)
	s.Bytes += n
	return n, err
}

// Unwrap lets http.ResponseController (used by ReverseProxy for flushing) reach the real writer.
func (s *StatusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
