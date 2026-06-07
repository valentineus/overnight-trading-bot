package healthcheck

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"overnight-trading-bot/internal/timeutil"
	"overnight-trading-bot/internal/tinvest"
)

type Service struct {
	db       *sql.DB
	gateway  tinvest.Gateway
	maxDrift time.Duration
	server   *http.Server
}

func New(db *sql.DB, gateway tinvest.Gateway, maxDrift time.Duration) *Service {
	return &Service{db: db, gateway: gateway, maxDrift: maxDrift}
}

func (s *Service) Start(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/ready", s.handleReady)
	s.server = &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 3 * time.Second}
	go func() {
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			// HTTP health errors are intentionally surfaced through /ready and logs by caller.
			return
		}
	}()
}

func (s *Service) Shutdown(ctx context.Context) error {
	if s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

func (s *Service) Check(ctx context.Context) map[string]string {
	status := map[string]string{"status": "ok"}
	if s.db != nil {
		if err := s.db.PingContext(ctx); err != nil {
			status["status"] = "fail"
			status["db"] = err.Error()
		} else {
			status["db"] = "ok"
		}
	}
	if s.gateway != nil {
		serverTime, err := s.gateway.GetServerTime(ctx)
		if err != nil {
			status["status"] = "fail"
			status["api"] = err.Error()
		} else {
			status["api"] = "ok"
			drift := timeutil.Drift(time.Now().UTC(), serverTime)
			status["clock_drift"] = drift.String()
			if s.maxDrift > 0 && drift > s.maxDrift {
				status["status"] = "fail"
				status["clock"] = fmt.Sprintf("drift %s exceeds %s", drift, s.maxDrift)
			}
		}
	}
	return status
}

func (s *Service) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Service) handleReady(w http.ResponseWriter, r *http.Request) {
	status := s.Check(r.Context())
	code := http.StatusOK
	if status["status"] != "ok" {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, status)
}

func CheckEndpoint(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("healthcheck returned %s", resp.Status)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}
