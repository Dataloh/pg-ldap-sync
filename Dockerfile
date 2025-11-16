# --- Build Stage ---
# Use an official Go image as the builder.
# THE FIX IS HERE: Updated from 1.22 to 1.24 to match your go.mod requirement.
FROM golang:1.24-alpine AS builder

# Set the working directory inside the container
WORKDIR /src

# Copy go.mod and go.sum files and download dependencies.
# This leverages Docker's layer caching.
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the application source code
COPY . .

# Build the application into a static binary.
# CGO_ENABLED=0 is crucial for creating a static binary without C dependencies.
# -ldflags "-s -w" strips debugging information, making the binary smaller.
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /app/pg-ldap-sync ./cmd/sync/main.go

# --- Final Stage ---
# Use alpine as it's small and includes user management tools
FROM alpine:latest

# Create a non-root user and group named 'appuser'
# We use a static UID/GID (e.g., 1001) for predictability
RUN addgroup -S appgroup -g 1001 && adduser -S appuser -u 1001 -G appgroup

# Set the working directory
WORKDIR /app

# Copy the binary from the builder stage and set ownership to our new user
COPY --from=builder --chown=appuser:appgroup /app/pg-ldap-sync .

# Make sure the user can read it.
COPY --chown=appuser:appgroup config.yml /opt/pg-ldap-sync/config.yml

# Switch to the non-root user
USER appuser

# Set the entrypoint
ENTRYPOINT ["/app/pg-ldap-sync"]
