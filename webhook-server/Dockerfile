# syntax=docker/dockerfile:experimental
# ---
FROM golang:1.18 AS build

ENV GOOS=linux
ENV GOARCH=amd64
ENV CGO_ENABLED=0

WORKDIR /work
COPY . /work

# Build admission-webhook
RUN --mount=type=cache,target=/root/.cache/go-build,sharing=private \
  go build -o bin/admission-webhook .

# ---
FROM alpine:3.16.0 AS run

COPY --from=build /work/bin/admission-webhook /usr/local/bin/
COPY config.yaml /home

RUN mkdir /home/public-keys

RUN apk --no-cache add tzdata

WORKDIR /home

CMD ["admission-webhook"]