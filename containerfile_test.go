package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// El Containerfile tiene que parsear: cada línea es una instrucción, un
// comentario, una continuación o está vacía. Un build local verde no lo
// prueba (go test no lee el Containerfile) y kaniko lo rechaza recién en
// Jenkins: pasó el 25-09 con una referencia de imagen suelta en la línea 24.
func TestContainerfileParsea(t *testing.T) {
	datos, err := os.ReadFile("Containerfile")
	if err != nil {
		t.Skip("sin Containerfile (dentro de la imagen no se copia)")
	}
	instr := regexp.MustCompile(`^(FROM|RUN|COPY|ADD|ENV|ARG|WORKDIR|USER|EXPOSE|ENTRYPOINT|CMD|LABEL|VOLUME|SHELL|HEALTHCHECK|STOPSIGNAL|ONBUILD)\b`)
	continua := false
	froms := 0
	for i, l := range strings.Split(string(datos), "\n") {
		s := strings.TrimSpace(l)
		switch {
		case continua:
		case s == "" || strings.HasPrefix(s, "#"):
		case instr.MatchString(s):
			if strings.HasPrefix(s, "FROM ") {
				froms++
				if !strings.Contains(s, "registry.registry-system.svc.cluster.local:5000/") || !strings.Contains(s, "@sha256:") {
					t.Errorf("línea %d: el FROM tiene que venir del registro interno y por digest: %s", i+1, s)
				}
			}
		default:
			t.Errorf("línea %d no es una instrucción: %q", i+1, s)
		}
		continua = strings.HasSuffix(s, "\\")
	}
	if froms != 2 {
		t.Errorf("esperaba 2 FROM (build y runtime), hay %d", froms)
	}
}
