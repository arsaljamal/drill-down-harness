# Used by GoReleaser: the drill binary is built outside and copied in.
FROM gcr.io/distroless/static-debian12:nonroot
COPY drill /usr/local/bin/drill
WORKDIR /work
ENTRYPOINT ["/usr/local/bin/drill"]
