package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"booking-service/internal/bookings"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = (&url.URL{
			Scheme:   "postgres",
			User:     url.UserPassword(env("DB_USER", "postgres"), env("DB_PASSWORD", "postgres")),
			Host:     net.JoinHostPort(env("DB_HOST", "localhost"), env("DB_PORT", "5432")),
			Path:     "/" + env("DB_NAME", "app"),
			RawQuery: "sslmode=disable",
		}).String()
	}

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	if err := bookings.Migrate(ctx, db); err != nil {
		log.Fatalf("migrate database: %v", err)
	}

	store := bookings.NewStore(db)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /bookings", store.Create)
	mux.HandleFunc("GET /bookings", store.List)
	mux.HandleFunc("PATCH /bookings/{id}", store.Reschedule)
	mux.HandleFunc("DELETE /bookings/{id}", store.Cancel)

	server := &http.Server{
		Addr:              ":" + env("APP_PORT", "8080"),
		Handler:           logging(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("booking service listening on %s", server.Addr)
		serverErr <- server.ListenAndServe()
	}()

	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP server: %v", err)
		}
	case <-sigCtx.Done():
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("graceful shutdown: %v", err)
		}
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(started))
	})
}
