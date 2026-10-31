FROM alpine:3.24.3@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
RUN apk add --no-cache samba-server=4.23.8-r0 samba-common-tools=4.23.8-r0 tini=0.19.0-r3
COPY smb.conf /etc/samba/smb.conf
COPY entrypoint.sh /entrypoint.sh
RUN chmod 0755 /entrypoint.sh
EXPOSE 445
ENTRYPOINT ["/sbin/tini", "--", "/entrypoint.sh"]
