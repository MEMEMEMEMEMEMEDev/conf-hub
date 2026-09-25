package main

// El bus: todo lo que el hub y el motor se dicen pasa por redis, y SOLO por
// acá. Las claves son el contrato entre los dos repos; el motor tiene la
// misma lista en motor/bus.py y un test en cada lado que la fija.
//
//   conf:salas             SET de ids
//   conf:sala:{id}         HASH  nombre idioma backend demo creada glosario
//   conf:tramos            STREAM hub → motor  (grupo de consumo "motor")
//                            sala idioma backend seq final t_ini t_fin glosario pcm
//   conf:subs:{id}         STREAM motor → hub  (MAXLEN ~ 5000)
//                            tipo(final|parcial|saltado|error) orig idioma
//                            es en t_ini t_fin backend lat_ms seq detalle
//   conf:motor             HASH  latido(ms) gpu backend cola saltados errores

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	claveSalas  = "conf:salas"
	claveTramos = "conf:tramos"
	claveMotor  = "conf:motor"
	maxSubs     = 5000
	maxTramos   = 2000 // ~ minutos de cola; lo viejo el motor lo saltaría igual
)

func claveSala(id string) string { return "conf:sala:" + id }
func claveSubs(id string) string { return "conf:subs:" + id }

type Sala struct {
	ID      string `json:"id"`
	Nombre  string `json:"nombre"`
	Idioma  string `json:"idioma"`  // idioma de la charla: en | es
	Backend string `json:"backend"` // gpu | cpu | gemini
	Demo    bool   `json:"demo"`
	Creada  int64  `json:"creada"`
	// Nombres propios y términos, separados por comas. El motor se los da a
	// Whisper como hotwords. Medido por experimental: con tramos largos sube
	// el acierto de esos términos (74 % → 95 %); con tramos cortos los mete
	// donde no van. Por eso es por sala y opcional.
	Glosario string `json:"glosario"`
}

// CamposTramo y CamposSub son el contrato con conf-motor (motor/bus.py,
// CAMPOS_TRAMO y CAMPOS_SUB). TestContrato los fija de este lado; el gemelo
// en tests/test_motor.py, del otro.
var (
	CamposTramo = []string{"sala", "idioma", "backend", "seq", "final", "t_ini", "t_fin", "glosario", "pcm"}
	CamposSub   = []string{"tipo", "orig", "idioma", "es", "en", "t_ini", "t_fin", "backend", "lat_ms", "seq", "detalle"}
)

// Subtitulo es una entrada de conf:subs:{id}, tal cual la escribe el motor.
type Subtitulo struct {
	ID      string `json:"id"` // id del stream: es el Last-Event-ID del SSE
	Tipo    string `json:"tipo"`
	Orig    string `json:"orig"`
	Idioma  string `json:"idioma"`
	Es      string `json:"es"`
	En      string `json:"en"`
	TIni    int64  `json:"t_ini"`
	TFin    int64  `json:"t_fin"`
	Backend string `json:"backend"`
	LatMs   int64  `json:"lat_ms"`
	Seq     int64  `json:"seq"`
	Detalle string `json:"detalle,omitempty"`
}

type EstadoMotor struct {
	Latido   int64  `json:"latido"`
	GPU      string `json:"gpu"`
	Backend  string `json:"backend"`
	Cola     int64  `json:"cola"`
	Saltados int64  `json:"saltados"`
	Errores  int64  `json:"errores"`
	Vivo     bool   `json:"vivo"`
	HaceSeg  int64  `json:"hace_s"`
}

type Bus struct{ r *redis.Client }

func NuevoBus(url string) (*Bus, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("REDIS_URL: %w", err)
	}
	return &Bus{r: redis.NewClient(opt)}, nil
}

func (b *Bus) Ping(ctx context.Context) error { return b.r.Ping(ctx).Err() }

// ---- salas ----------------------------------------------------------------

func (b *Bus) GuardarSala(ctx context.Context, s Sala) error {
	demo := "0"
	if s.Demo {
		demo = "1"
	}
	p := b.r.TxPipeline()
	p.HSet(ctx, claveSala(s.ID), "nombre", s.Nombre, "idioma", s.Idioma,
		"backend", s.Backend, "demo", demo, "creada", s.Creada, "glosario", s.Glosario)
	p.SAdd(ctx, claveSalas, s.ID)
	_, err := p.Exec(ctx)
	return err
}

var ErrNoExiste = errors.New("la sala no existe")

func (b *Bus) Sala(ctx context.Context, id string) (Sala, error) {
	m, err := b.r.HGetAll(ctx, claveSala(id)).Result()
	if err != nil {
		return Sala{}, err
	}
	if len(m) == 0 {
		return Sala{}, ErrNoExiste
	}
	creada, _ := strconv.ParseInt(m["creada"], 10, 64)
	return Sala{ID: id, Nombre: m["nombre"], Idioma: m["idioma"], Backend: m["backend"],
		Demo: m["demo"] == "1", Creada: creada, Glosario: m["glosario"]}, nil
}

func (b *Bus) Salas(ctx context.Context) ([]Sala, error) {
	ids, err := b.r.SMembers(ctx, claveSalas).Result()
	if err != nil {
		return nil, err
	}
	out := make([]Sala, 0, len(ids))
	for _, id := range ids {
		s, err := b.Sala(ctx, id)
		if errors.Is(err, ErrNoExiste) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func (b *Bus) CambiarBackend(ctx context.Context, id, backend string) error {
	if _, err := b.Sala(ctx, id); err != nil {
		return err
	}
	return b.r.HSet(ctx, claveSala(id), "backend", backend).Err()
}

func (b *Bus) BorrarSala(ctx context.Context, id string) error {
	p := b.r.TxPipeline()
	p.SRem(ctx, claveSalas, id)
	p.Del(ctx, claveSala(id))
	_, err := p.Exec(ctx)
	return err
}

// ---- tramos: hub → motor ----------------------------------------------------

func pcmABytes(pcm []int16) []byte {
	out := make([]byte, 2*len(pcm))
	for i, m := range pcm {
		binary.LittleEndian.PutUint16(out[2*i:], uint16(m))
	}
	return out
}

func (b *Bus) PublicarTramo(ctx context.Context, s Sala, t Tramo) error {
	final := "0"
	if t.Final {
		final = "1"
	}
	return b.r.XAdd(ctx, &redis.XAddArgs{
		Stream: claveTramos,
		MaxLen: maxTramos,
		Approx: true,
		Values: []any{
			"sala", s.ID, "idioma", s.Idioma, "backend", s.Backend,
			"seq", t.Seq, "final", final,
			"t_ini", t.Inicio.UnixMilli(), "t_fin", t.Fin.UnixMilli(),
			"glosario", s.Glosario,
			"pcm", pcmABytes(t.PCM),
		},
	}).Err()
}

// ---- subtítulos: motor → hub ------------------------------------------------

func subDeMensaje(m redis.XMessage) Subtitulo {
	v := func(k string) string { s, _ := m.Values[k].(string); return s }
	n := func(k string) int64 { x, _ := strconv.ParseInt(v(k), 10, 64); return x }
	return Subtitulo{ID: m.ID, Tipo: v("tipo"), Orig: v("orig"), Idioma: v("idioma"),
		Es: v("es"), En: v("en"), TIni: n("t_ini"), TFin: n("t_fin"), Backend: v("backend"),
		LatMs: n("lat_ms"), Seq: n("seq"), Detalle: v("detalle")}
}

// PublicarSubtitulo lo usa el motor falso de los tests y el modo dev; el
// motor real escribe lo mismo desde Python.
func (b *Bus) PublicarSubtitulo(ctx context.Context, sala string, s Subtitulo) (string, error) {
	return b.r.XAdd(ctx, &redis.XAddArgs{
		Stream: claveSubs(sala), MaxLen: maxSubs, Approx: true,
		Values: []any{"tipo", s.Tipo, "orig", s.Orig, "idioma", s.Idioma, "es", s.Es, "en", s.En,
			"t_ini", s.TIni, "t_fin", s.TFin, "backend", s.Backend, "lat_ms", s.LatMs,
			"seq", s.Seq, "detalle", s.Detalle},
	}).Result()
}

// Ultimos devuelve los últimos n subtítulos finales, en orden cronológico.
func (b *Bus) Ultimos(ctx context.Context, sala string, n int64) ([]Subtitulo, error) {
	ms, err := b.r.XRevRangeN(ctx, claveSubs(sala), "+", "-", n*3).Result()
	if err != nil {
		return nil, err
	}
	var out []Subtitulo
	for _, m := range ms {
		s := subDeMensaje(m)
		if s.Tipo == "final" {
			out = append(out, s)
			if int64(len(out)) == n {
				break
			}
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// Desde devuelve todo lo posterior a id (exclusivo): el replay de una
// reconexión con Last-Event-ID.
func (b *Bus) Desde(ctx context.Context, sala, id string) ([]Subtitulo, error) {
	ms, err := b.r.XRange(ctx, claveSubs(sala), "("+id, "+").Result()
	if err != nil {
		return nil, err
	}
	out := make([]Subtitulo, 0, len(ms))
	for _, m := range ms {
		out = append(out, subDeMensaje(m))
	}
	return out, nil
}

// Todos devuelve la charla entera, para exportarla.
func (b *Bus) Todos(ctx context.Context, sala string) ([]Subtitulo, error) {
	return b.Desde(ctx, sala, "0-0")
}

// Esperar bloquea hasta que haya algo después de id o venza el plazo.
func (b *Bus) Esperar(ctx context.Context, sala, id string, plazo time.Duration) ([]Subtitulo, error) {
	res, err := b.r.XRead(ctx, &redis.XReadArgs{
		Streams: []string{claveSubs(sala), id}, Block: plazo, Count: 100,
	}).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Subtitulo
	for _, st := range res {
		for _, m := range st.Messages {
			out = append(out, subDeMensaje(m))
		}
	}
	return out, nil
}

// UltimoID es el id más reciente del stream de la sala ("0-0" si vacío).
func (b *Bus) UltimoID(ctx context.Context, sala string) (string, error) {
	ms, err := b.r.XRevRangeN(ctx, claveSubs(sala), "+", "-", 1).Result()
	if err != nil {
		return "", err
	}
	if len(ms) == 0 {
		return "0-0", nil
	}
	return ms[0].ID, nil
}

// ---- el motor ----------------------------------------------------------------

// MotorVivoHasta: cuánto puede faltar el latido antes de declarar el motor
// muerto. Mayor que lo que tarda un Recreate en cargar los pesos: durante un
// despliegue del motor la sala está DEGRADADA, no muerta (plataforma, D-01).
const MotorVivoHasta = 120 * time.Second

func (b *Bus) Motor(ctx context.Context, ahora time.Time) (EstadoMotor, error) {
	m, err := b.r.HGetAll(ctx, claveMotor).Result()
	if err != nil {
		return EstadoMotor{}, err
	}
	n := func(k string) int64 { x, _ := strconv.ParseInt(m[k], 10, 64); return x }
	e := EstadoMotor{Latido: n("latido"), GPU: m["gpu"], Backend: m["backend"],
		Cola: n("cola"), Saltados: n("saltados"), Errores: n("errores")}
	if e.Latido > 0 {
		hace := ahora.Sub(time.UnixMilli(e.Latido))
		e.HaceSeg = int64(hace.Seconds())
		e.Vivo = hace < MotorVivoHasta
	}
	return e, nil
}
