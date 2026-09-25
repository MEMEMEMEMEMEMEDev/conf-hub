# conf-hub — a Go service in two stages, built and run on images that
# came in through the mirror.
#
# WHY THE TWO FROMs ARE PLACEHOLDERS. Until 2026-08-29 they read
# `FROM docker.io/library/golang:1.26-alpine` and
# `FROM docker.io/library/alpine:3.21`: pulled off the internet, by a
# mutable tag, in the pipeline that afterwards signs the result with the
# aegis key. It is exactly what mirror-images/images.txt exists to
# forbid, and it was the template teaching it to every new organization.
# `aegis app new` resolves each one against the internal registry at
# instantiation time and writes the reference pinned BY DIGEST — a
# digest that is NOT upstream's, because the mirror rebuilds the
# manifest as it copies, which is why nothing in this tree can compute
# it offline.
#
# The toolchain pin ages, and that is watched: the pipeline's Trivy scan
# blocks when the compiler's stdlib piles up CVEs, and that red build is
# the signal to mirror a newer golang and re-instantiate this line. No
# line of your code can fix a CVE that belongs to the compiler.
FROM registry.registry-system.svc.cluster.local:5000/golang:1.26.6-alpine@sha256:db4eef83f512671526c4494a1e8c29b7f6386ad8354e71a279f3ee44cfcb447b AS build
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -o /app .

FROM registry.registry-system.svc.cluster.local:5000/alpine:3.21@sha256:90794469f3b3982e5f921d2cbe738b5e91b75d651686fe1bc6ec7fd2b9082e4a
COPY --from=build /app /usr/local/bin/app
# Non-root ALWAYS, and NUMERIC: the organization's namespace is PSS
# restricted, so a root container does not even get to start, and a
# `USER name` fails runAsNonRoot because the kubelet cannot read the
# image's /etc/passwd to prove the name is not root.
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/app"]
