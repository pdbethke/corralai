# The read-only observer (corral-observe). Pure Go (no CGO) — it is just a
# credentialed reverse proxy — so it builds static and ships on distroless.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/corral-observe ./cmd/corral-observe

FROM gcr.io/distroless/static-debian12

# Bind all interfaces inside the container; the host publishes the port.
ENV CORRAL_OBSERVE_ADDR=0.0.0.0:8080
EXPOSE 8080

COPY --from=build /out/corral-observe /corral-observe

# The binary self-checks (no shell / curl needed on distroless): it probes the
# brain's /healthz through itself, so an unreachable brain marks the container
# unhealthy.
HEALTHCHECK --interval=30s --timeout=6s --start-period=5s --retries=3 \
	CMD ["/corral-observe", "-ping"]

ENTRYPOINT ["/corral-observe"]
