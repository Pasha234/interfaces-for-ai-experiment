package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"booking-service/internal/httpapi"
	"booking-service/internal/postgres"
	"booking-service/internal/service"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	db, err := sql.Open("pgx", databaseURL())
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	startupCtx, cancelStartup := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelStartup()
	if err := db.PingContext(startupCtx); err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	if err := postgres.Migrate(startupCtx, db); err != nil {
		log.Fatalf("migrate database: %v", err)
	}

	bookingService := service.NewBookingService(postgres.NewBookingRepository(db))
	mux := http.NewServeMux()
	httpapi.NewBookingHandler(bookingService).Register(mux)
	server := &http.Server{
		Addr:              ":" + envOr("APP_PORT", "8080"),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() { serverErr <- server.ListenAndServe() }()

	stopCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http server: %v", err)
		}
	case <-stopCtx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("http server shutdown: %v", err)
		}
	}
}

func databaseURL() string {
	if value := os.Getenv("DATABASE_URL"); value != "" {
		return value
	}
	return "postgres://" + envOr("DB_USER", "postgres") + ":" + envOr("DB_PASSWORD", "postgres") +
		"@" + envOr("DB_HOST", "localhost") + ":" + envOr("DB_PORT", "5432") + "/" +
		envOr("DB_NAME", "app") + "?sslmode=disable"
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
