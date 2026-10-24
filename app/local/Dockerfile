# Context is the output of app/local/build-bundle.sh, not the source checkout.
FROM scratch
COPY employee /usr/local/bin/employee
COPY employee-eval /usr/local/bin/employee-eval
COPY cases/ /opt/employee/cases/
COPY ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --chmod=1777 tmp/ /tmp/
WORKDIR /work
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/employee"]
