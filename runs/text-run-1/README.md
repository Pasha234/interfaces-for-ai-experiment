# Booking service

HTTP service for managing meeting room bookings. It uses PostgreSQL and listens on port `8080` by default.

## Run with Docker Compose

```sh
docker compose up --build
```

The app waits for PostgreSQL, creates the `bookings` table on startup, then listens on `http://localhost:8080`. Connection settings can be changed with `POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_DB`, `POSTGRES_PORT`, and `APP_PORT`.

To run the Go service directly, set `DATABASE_URL` (or `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`, and `DB_NAME`) and run `go run .`.

## API

Create a booking:

```sh
curl -i http://localhost:8080/bookings \
  -H 'Content-Type: application/json' \
  -d '{"room_id":"room-1","user_id":"user-1","start_time":"2026-11-23T10:00:00Z","end_time":"2026-11-23T11:00:00Z"}'
```

Returns `201` with the booking and its generated `id`; invalid input returns `400`, and an overlapping booking for the same room returns `409`. Intervals are half-open, so one booking may end exactly when the next begins.

List active bookings, optionally filtered by room:

```sh
curl 'http://localhost:8080/bookings?room_id=room-1'
```

Cancel a booking:

```sh
curl -i -X DELETE http://localhost:8080/bookings/1
```

Returns `204` on success and `404` when the booking does not exist or has already been canceled. Canceled bookings are excluded from listings and no longer block the room.
