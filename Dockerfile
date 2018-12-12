FROM imqs/ubuntu-base
RUN mkdir -p /var/log/imqs
COPY bin/router-core /opt/router
ENV IMQS_CONTAINER=true
EXPOSE 80
ENTRYPOINT ["wait-for-nc.sh", "config:80", "--", "/opt/router"]

