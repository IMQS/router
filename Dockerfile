# docker build -t imqs/router:master --build-arg ssh_pvt_key="`cat ~/secrets/deploybot/id_rsa`" .

##################################
# builder image
##################################
FROM golang:1.12 as builder

ARG ssh_pvt_key

RUN mkdir /build

# Authorize SSH Host
RUN mkdir -p /root/.ssh && \
    chmod 0700 /root/.ssh && \
    ssh-keyscan github.com > /root/.ssh/known_hosts

# We need this key so that we can read our private IMQS git repos from github
RUN echo "$ssh_pvt_key" > /root/.ssh/id_rsa && \
    chmod 600 /root/.ssh/id_rsa

RUN git config --global url."git@github.com:".insteadOf "https://github.com/"

# First step, just create a dummy 'main.go', but copy over our real 'go.mod',
# so that we can do a fake initial build, which will pull all of our dependencies
COPY go.mod /build/
RUN echo "package main" > /build/main.go

# This just pulls the dependencies, and caches them
WORKDIR /build/
RUN go build main.go || true

# Copy over our actual sources, and do the real build
# Our Go packages will be cached from the previous dummy build.
COPY ./ /build

WORKDIR /build/
RUN go build main.go

####################################
# deployed image
####################################
FROM imqs/ubuntu-base
COPY --from=builder /build/main /opt/router
EXPOSE 80
EXPOSE 443
ENTRYPOINT ["wait-for-nc.sh", "config:80", "--", "/opt/router"]
