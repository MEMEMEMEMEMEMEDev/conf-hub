# conf-hub — la puerta HTTP de conf: ingesta de audio, segmentador, SSE,
# panel y export. Go en dos etapas, sobre imágenes que entraron por el
# espejo de la plataforma.
#
# LOS DOS FROM LOS ESCRIBE `aegis app new` (plantilla base), fijados por
# digest contra el registro interno. No se copian de otro repo.
FROM registry.registry-system.svc.cluster.local:5000/golang:1.26.6-alpine@sha256:db4eef83f512671526c4494a1e8c29b7f6386ad8354e71a279f3ee44cfcb447b AS build
WORKDIR /src
# vendor/ viaja en el repo: el build no sale a proxy.golang.org.
COPY go.mod go.sum ./
COPY vendor ./vendor
COPY *.go ./
COPY demo ./demo
# El front ya construido (npm run build en web/): el binario lo embebe.
COPY web/dist ./web/dist
# LA PUERTA DE LA IMAGEN: segmentador (sintético y sobre el audio real de
# la demo), SSE con Last-Event-ID, permisos 401/403, salud, export. Sobre
# un redis en memoria (miniredis, sólo en tests). Rojo = no hay imagen.
RUN CGO_ENABLED=0 go test -mod=vendor -count=1 ./...
# SIN el tag `dev`: el redis en memoria de desarrollo no entra al binario.
RUN CGO_ENABLED=0 go build -mod=vendor -trimpath -ldflags="-s -w" -o /hub .

FROM registry.registry-system.svc.cluster.local:5000/alpine:3.21@sha256:90794469f3b3982e5f921d2cbe738b5e91b75d651686fe1bc6ec7fd2b9082e4a
registry.registry-system.svc.cluster.local:5000/alpine:3.21@sha256:90794469f3b3982e5f921d2cbe738b5e91b75d651686fe1bc6ec7fd2b9082e4a
COPY --from=build /hub /usr/local/bin/hub
# Numérico y no-root: PSS restricted.
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/hub"]
