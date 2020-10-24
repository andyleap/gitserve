FROM golang:alpine as builder
RUN mkdir /build
WORKDIR /build
ADD go.mod go.sum /build/
RUN go mod download
ADD . /build/
RUN go build -o gitserve ./
FROM alpine
COPY --from=builder /build/gitserve /app/
COPY templates /app/templates
WORKDIR /app
CMD ["./gitserve"]
