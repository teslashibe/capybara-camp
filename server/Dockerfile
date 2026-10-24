# Build and runtime images are pinned for the V1 candidate.
FROM golang:1.26@sha256:672c944cfd07599b5f1bb179869270cdc94c6254e41db4d431259693cb286b82 AS build
WORKDIR /src
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/camp-server ./cmd/camp-server && \
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/camp-migrate ./cmd/migrate && \
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/camp-controller ./cmd/camp-controller

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/ /
USER 65532:65532
EXPOSE 8096
ENTRYPOINT ["/camp-server"]
