FROM golang:1.16.7-alpine3.14 AS builder

WORKDIR /app
COPY . .
RUN CGO_ENABLED=no go build -o /lkebot .

FROM alpine:3.14 AS kubectl
RUN apk --no-cache add curl \
 && curl -Lo /kubectl https://storage.googleapis.com/kubernetes-release/release/v1.20.0/bin/linux/amd64/kubectl \
 && chmod 0755 /kubectl

FROM alpine:3.14
RUN apk --no-cache add bash
COPY --from=kubectl /kubectl /usr/bin/kubectl

COPY cleanup /usr/bin/cleanup
COPY --from=builder /lkebot /lkebot

CMD ["/lkebot"]
