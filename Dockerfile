# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /app/server ./cmd/server

# Runtime needs ffmpeg for server-side video→MP4 transcoding, so use alpine
# (distroless/static has no ffmpeg). The Go binary is static (CGO disabled).
# NOTE: temp disable ffmpeg to reduce image size, as it's only used for transcoding videos to MP4, and we can do that in a separate step before uploading to the server.
# FROM alpine:3.20
# RUN apk add --no-cache ffmpeg ca-certificates \
# 	&& adduser -D -u 10001 appuser
COPY --from=build /app/server /server
USER appuser
EXPOSE 8080
ENTRYPOINT ["/server"]
