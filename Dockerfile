# Stage 1: Build the application
FROM golang:1.14-buster as builder

RUN apt-get update && apt-get install -y protobuf-compiler && rm -rf /var/lib/apt/lists/*
RUN go get google.golang.org/protobuf/cmd/protoc-gen-go
RUN go get go get google.golang.org/grpc/cmd/protoc-gen-go-grpc

RUN mkdir /build
RUN mkdir -p /seabird-irc-backend/{go,proto}

WORKDIR /seabird-irc-backend

ADD ./go.mod ./go.sum ./
RUN go mod download

ADD ./proto/* /seabird-irc-backend/proto/

ADD ./pb/* ./pb/
RUN go generate ./...

ADD . ./
RUN go build -v -o /build/seabird-irc-backend ./cmd/seabird-irc-backend

# Stage 2: Copy files and configure what we need
FROM debian:buster-slim

RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*

# Copy the built seabird into the container
COPY --from=builder /build /bin

EXPOSE 11235

ENTRYPOINT ["/bin/seabird-irc-backend"]
