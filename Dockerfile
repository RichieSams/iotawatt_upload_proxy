# Now create the "real" docker image
FROM ubuntu:22.04

# Install ca-certificates for server usage
RUN apt-get update -y && apt-get install -y \
    ca-certificates \
    curl \
    tini \
    && rm -rf /var/lib/apt/lists/*

# Copy the binary we built from the builder image
COPY iup /usr/local/bin/iup

# Default to running the `serve` command of our binary
ENTRYPOINT ["/usr/bin/tini", "-g", "--"]
CMD ["/usr/local/bin/iup"]
