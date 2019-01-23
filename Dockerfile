##################################
# builder image
##################################
FROM golang:1.8 as builder
RUN mkdir /build
COPY src/ /build/src
ENV GOPATH /build
WORKDIR /build/
RUN go install github.com/IMQS/router-core

####################################
# deployed image
####################################
FROM imqs/ubuntu-base
RUN mkdir -p /var/log/imqs
COPY --from=builder /build/bin/router-core /opt/router
EXPOSE 80
ENTRYPOINT ["wait-for-nc.sh", "config:80", "--", "/opt/router"]

