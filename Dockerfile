FROM alpine:3.5
RUN mkdir -p /var/log/imqs
COPY bin/router-core /opt/router
EXPOSE 80
ENTRYPOINT ["/opt/router", "-container"]

