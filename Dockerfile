FROM golang:1.14.11-alpine3.11 AS builder

WORKDIR /app
COPY . .
RUN CGO_ENABLED=no go build -o /lkebot .

FROM alpine:3.11
COPY --from=builder /lkebot /lkebot
ADD cleanup /usr/bin/cleanup
CMD ["/lkebot"]
