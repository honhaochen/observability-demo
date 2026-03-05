FROM golang:1.25-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
COPY vendor ./vendor
COPY . .
RUN CGO_ENABLED=0 go build -mod=vendor -o /bin/api .

FROM alpine:3.21
COPY --from=build /bin/api /api
EXPOSE 8081
ENTRYPOINT ["/api"]
