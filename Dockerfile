# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /app/server ./cmd/server

# Runtime needs ffmpeg for server-side video→MP4 transcoding, so use alpine
# (distroless/static has no ffmpeg). The Go binary is static (CGO disabled).
FROM alpine:3.20
RUN apk add --no-cache ffmpeg ca-certificates \
	&& adduser -D -u 10001 appuser
COPY --from=build /app/server /server
USER appuser
EXPOSE 8080
ENTRYPOINT ["/server"]
