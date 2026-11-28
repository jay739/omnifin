FROM --platform=$BUILDPLATFORM docker.io/hrfee/jfa-go-build-docker:latest AS support
ARG VERSION
ARG REVISION
ARG BUILD_TIME
ARG BUILD_DATE
ARG BUILT_BY=github-actions
ENV JFA_GO_VERSION=$VERSION JFA_GO_COMMIT=$REVISION JFA_GO_BUILD_TIME=$BUILD_TIME JFA_GO_BUILD_MODE=docker JFA_GO_BUILT_BY=$BUILT_BY
COPY . /opt/build
RUN test -n "$BUILD_DATE" && cd /opt/build && INTERNAL=off UPDATER=docker ./scripts/version.sh goreleaser build --snapshot --skip=validate --clean --id notray
RUN sed -i 's#id="password_resets-watch_directory" placeholder="/config/jellyfin"#id="password_resets-watch_directory" value="/jf" disabled#g' /opt/build/build/data/html/setup.html

FROM gcr.io/distroless/base:latest AS final
ARG TARGETARCH
ARG VERSION
ARG REVISION
ARG BUILD_DATE
ARG SOURCE_URL=https://github.com/jay739/omnifin
LABEL org.opencontainers.image.version=$VERSION org.opencontainers.image.revision=$REVISION org.opencontainers.image.created=$BUILD_DATE org.opencontainers.image.source=$SOURCE_URL
COPY --from=support /opt/build/dist/*_linux_${TARGETARCH}* /jfa-go
COPY --from=support /opt/build/build/data /jfa-go/data
EXPOSE 8056 8057

HEALTHCHECK --interval=30s --timeout=5s --start-period=120s --retries=3 CMD ["/jfa-go/omnifin", "-healthcheck"]

CMD [ "/jfa-go/omnifin", "-data", "/data" ]
