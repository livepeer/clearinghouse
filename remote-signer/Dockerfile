# go-livepeer remote signer — extends livepeer/go-livepeer (CUDA libs required by binary).
# stable is the release channel. Keep the tag with the digest: a digest with no
# tag is what Dependabot resolves against latest.
# Do not copy livepeer into debian:slim; the binary links libnppig.so and other CUDA NPP libs.
#
# go-livepeer runs directly in this container. Only the signing HTTP port is EXPOSE'd
# and published. The CLI/admin port (-cliAddr) is bound to loopback inside the
# container and is never exposed or published — not here, not in docker-compose.
FROM livepeer/go-livepeer:stable@sha256:972870bed3f302ee69ada1e719d7767274a77d2e752be1ab82581ddf2365dfe7

RUN mkdir -p /data

COPY remote-signer/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 8081
VOLUME ["/data"]

ENTRYPOINT ["/entrypoint.sh"]
