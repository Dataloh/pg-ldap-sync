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
# Use a minimal 'scratch' image which contains nothing, for maximum security and smallest size.
FROM scratch

# Set the working directory
WORKDIR /app

# Copy the compiled binary from the builder stage
COPY --from=builder /app/pg-ldap-sync .

# Copy the default configuration file into the image.
# This serves as a fallback and as documentation. In a real deployment,
# this file will be overridden by a volume mount.
COPY config.yml .

# Set the entrypoint for the container. When the container starts, it will run this command.
ENTRYPOINT ["/app/pg-ldap-sync"]

