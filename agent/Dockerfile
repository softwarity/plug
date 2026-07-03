FROM alpine:3.21

# openssh: transport for sshuttle | python3: sshuttle server side | iproute2: subnet discovery
RUN apk add --no-cache openssh-server python3 iproute2 \
    && adduser -D -s /bin/sh plug \
    && sed -i 's/^plug:!/plug:*/' /etc/shadow \
    && { \
         echo 'PasswordAuthentication no'; \
         echo 'PermitRootLogin no'; \
         echo 'AllowUsers plug'; \
       } >> /etc/ssh/sshd_config

COPY --chown=plug:plug --chmod=600 authorized_keys /home/plug/.ssh/authorized_keys
RUN chmod 700 /home/plug/.ssh
COPY --chmod=755 entrypoint.sh /entrypoint.sh

EXPOSE 22
ENTRYPOINT ["/entrypoint.sh"]
