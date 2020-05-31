FROM golang:1.13-alpine AS build

COPY . /go/src/github.com/jlewi/cloud-endpoints-controller/
WORKDIR /go/src/github.com/jlewi/cloud-endpoints-controller/cmd/
RUN go install .

FROM alpine:3.7
RUN apk add --update ca-certificates bash curl
COPY --from=build /go/bin/cmd /usr/bin/cloud-endpoints-controller
CMD ["/usr/bin/cloud-endpoints-controller"]