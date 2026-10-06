FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mmt ./cmd/mmt

FROM alpine:3.20
RUN adduser -D -H app
COPY --from=build /out/mmt /usr/local/bin/mmt
USER app
EXPOSE 8081
HEALTHCHECK --interval=10s --timeout=3s --retries=5 CMD wget -qO- http://localhost:8081/healthz || exit 1
ENTRYPOINT ["mmt"]
