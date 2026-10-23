# zaweb, from the static binary `make build` leaves in bin/.  No shell, no
# libc, nothing else in the image.
FROM scratch
COPY bin/zaweb /zaweb
USER 8460:8460
EXPOSE 8460
ENTRYPOINT ["/zaweb"]
