# go-livepeer remote signer — extends livepeer/go-livepeer (CUDA libs required by binary).
# Pin matches pymthouse signer-dmz (sha-264fc6ba8c67c03cd53824d55b13a53721f07577).
# Do not copy livepeer into debian:slim; the binary links libnppig.so and other CUDA NPP libs.
#
# go-livepeer runs directly in this container. Only the signing HTTP port is EXPOSE'd
# and published. The CLI/admin port (-cliAddr) is bound to loopback inside the
# container and is never exposed or published — not here, not in docker-compose.
FROM livepeer/go-livepeer:sha-264fc6ba8c67c03cd53824d55b13a53721f07577

RUN mkdir -p /data

COPY remote-signer/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 8081
VOLUME ["/data"]

ENTRYPOINT ["/entrypoint.sh"]
