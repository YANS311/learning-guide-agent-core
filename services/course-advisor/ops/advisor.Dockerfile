FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
COPY advisor ./advisor
COPY web ./web
COPY testdata/handbook.json ./testdata/handbook.json
RUN CGO_ENABLED=0 go build -trimpath -o /out/advisor .

FROM alpine:3.22
COPY --from=build /out/advisor /usr/local/bin/advisor
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/advisor"]
