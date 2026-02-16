# docker build -t imqs/router:latest --ssh default .

##################################
# builder image
##################################
FROM golang:1.22 AS builder

RUN mkdir /build
WORKDIR /build

# Authorize SSH Host
RUN mkdir -p /root/.ssh && \
    chmod 0700 /root/.ssh && \
    ssh-keyscan github.com > /root/.ssh/known_hosts

RUN --mount=type=ssh \
	git config --global url."git@github.com:".insteadOf "https://github.com/"

# Cache downloads
COPY go.mod go.sum /build/
RUN --mount=type=ssh \
	go mod download

# Compile
COPY . /build/
RUN go build -o router main.go

####################################
# deployed image
####################################
FROM imqs/ubuntu-base:24.04
COPY --from=builder /build/router /opt/router

EXPOSE 80
EXPOSE 443

HEALTHCHECK CMD curl --fail http://localhost/router/ping || exit 1

ENTRYPOINT ["wait-for-nc.sh", "config:80", "--", "/opt/router"]
