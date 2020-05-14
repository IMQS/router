# docker build -t imqs/router:master --build-arg SSH_KEY="`cat ~/.ssh/id_rsa`" .

##################################
# builder image
##################################
FROM golang:1.13 as builder

ARG SSH_KEY

RUN mkdir /build
WORKDIR /build

# Authorize SSH Host
RUN mkdir -p /root/.ssh && \
    chmod 0700 /root/.ssh && \
    ssh-keyscan github.com > /root/.ssh/known_hosts

# We need this key so that we can read our private IMQS git repos from github
RUN echo "$SSH_KEY" > /root/.ssh/id_rsa && \
    chmod 600 /root/.ssh/id_rsa

RUN git config --global url."git@github.com:".insteadOf "https://github.com/"

# Cache downloads
COPY go.mod go.sum /build/
RUN go mod download

# Compile
COPY . /build/
RUN go build -o router main.go

####################################
# deployed image
####################################
FROM imqs/ubuntu-base
COPY --from=builder /build/router /opt/router
EXPOSE 80
EXPOSE 443
ENTRYPOINT ["wait-for-nc.sh", "config:80", "--", "/opt/router"]
