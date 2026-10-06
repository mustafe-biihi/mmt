FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gateway .

FROM alpine:3.20
RUN adduser -D -H app
COPY --from=build /out/gateway /usr/local/bin/gateway
USER app
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --retries=5 CMD wget -qO- http://localhost:8080/healthz || exit 1
ENTRYPOINT ["gateway"]
