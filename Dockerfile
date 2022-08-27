FROM golang:1.17-stretch

WORKDIR /app

COPY go.mod ./
COPY go.sum ./
RUN go mod download

COPY *.go ./

RUN go build -o /kube-web-api

EXPOSE 3000

CMD ["/kube-web-api"]