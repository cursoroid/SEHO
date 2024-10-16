# Step 1: Use the official Golang image as the build stage
FROM golang:1.20-alpine AS builder

# Step 2: Set the working directory inside the container
WORKDIR /app

# Step 3: Copy the Go modules and download the dependencies
COPY go.mod go.sum ./
RUN go mod download

# Step 4: Copy the rest of the application code
COPY . .

# Step 5: Build the Go app
RUN go build -o main .

# Step 6: Use a smaller base image to reduce the final image size
FROM alpine:latest

# Step 7: Set the working directory for the smaller image
WORKDIR /app

# Step 8: Copy the binary from the build stage
COPY --from=builder /app/main .

# Step 9: Expose the port your app runs on
EXPOSE 8080

# Step 10: Define the command to run the Go binary
CMD ["./main"]
