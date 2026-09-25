package main

// El segmentador: convierte el audio continuo de una sala en TRAMOS que el
// motor transcribe de a uno.
//
// POR QUÉ 8 A 12 SEGUNDOS Y NO 3 O 4, que es lo que hace casi toda la
// competencia. Lo midió experimental (mediciones/LEEME.md §3): Whisper
// procesa siempre una ventana de 30 s, así que un tramo corto no ahorra
// cómputo, y cortar cada 4 s parte palabras y le quita contexto al modelo.
// El español pasó de 8,8 % de WER con tramos de 10 s a 20 % con tramos de
// 4 s. La latencia que eso cuesta la recuperan los PARCIALES (ver Parcial).
//
// EL ALGORITMO lo describe Subtitula (flordelcastillo/subtitula, Apache 2.0,
// README §Segmentador): energía en frames de 30 ms contra un piso de ruido
// que se adapta a la sala, cierre en una pausa, y si nadie hace pausa, corte
// en el frame más silencioso del último segundo y medio para no partir una
// palabra. Acá está reimplementado en Go con otros límites; el crédito de
// la idea es de ellos.

import (
	"math"
	"time"
)

const (
	FrecMuestreo  = 16000 // PCM s16le mono de punta a punta
	muestrasFrame = FrecMuestreo * 30 / 1000
	msFrame       = 30
)

// ConfigSegmentador son los límites. Salen de variables de entorno en
// main.go; los defaults son los que recomendó experimental.
type ConfigSegmentador struct {
	Min          time.Duration // no se cierra por pausa antes de esto
	Max          time.Duration // se corta sí o sí al llegar acá
	Pausa        time.Duration // silencio que cierra un tramo ya largo
	PausaLarga   time.Duration // silencio que cierra un tramo aunque sea corto
	VozMinima    time.Duration // menos voz que esto: el tramo no se manda
	MargenDB     float64       // cuánto sobre el piso de ruido es voz
	PisoAbsoluto float64       // dBFS: por debajo nunca es voz
	CadaParcial  time.Duration // 0 = sin parciales
	PrerrolloVoz time.Duration // audio que se guarda antes de la primera voz
}

func ConfigPorDefecto() ConfigSegmentador {
	return ConfigSegmentador{
		Min:          8 * time.Second,
		Max:          12 * time.Second,
		Pausa:        300 * time.Millisecond,
		PausaLarga:   1200 * time.Millisecond,
		VozMinima:    400 * time.Millisecond,
		MargenDB:     10,
		PisoAbsoluto: -50,
		PrerrolloVoz: 300 * time.Millisecond,
	}
}

// Tramo es un trozo de habla listo para transcribir. Inicio y Fin son
// tiempo de PARED del audio (no de cuándo se procesó): la latencia que se
// publica es "ahora - Fin", o sea cuánto detrás de la voz llega el texto.
type Tramo struct {
	PCM    []int16
	Inicio time.Time
	Fin    time.Time
	Seq    int64
	Final  bool // false = parcial: la ventana abierta, todavía creciendo
}

// Segmentador no es seguro para uso concurrente: cada sala tiene el suyo y
// lo alimenta una sola goroutine (la del WebSocket o la de la demo).
type Segmentador struct {
	cfg    ConfigSegmentador
	emitir func(Tramo)
	piso   float64 // dBFS, estimado del ruido de la sala
	pisoOK bool

	frame    []int16 // frame incompleto de la última escritura
	origen   time.Time
	muestras int64 // muestras recibidas desde origen: el reloj del audio

	abierto    bool
	buf        []int16
	energias   []float64 // una por frame de buf
	inicioBuf  int64     // muestra de origen del primer elemento de buf
	voz        int       // frames con voz en buf
	silencio   int       // frames seguidos de silencio al final de buf
	prerrollo  []int16
	seq        int64
	ultParcial int64 // muestra en la que se emitió el último parcial

	Nivel float64 // dBFS del último frame, para el medidor del panel
}

func NuevoSegmentador(cfg ConfigSegmentador, origen time.Time, emitir func(Tramo)) *Segmentador {
	// La secuencia arranca en los SEGUNDOS del reloj, no en 0. Un
	// segmentador nuevo nace cada vez que el hub se reinicia, un emisor
	// reconecta o una sala demo vuelve a tener público; el motor y el
	// navegador descartan el parcial de un tramo cuyo final ya vieron
	// (seq ≤ último final). Con seq desde 0, después de cualquier reinicio
	// los parciales de la sala quedaban suprimidos hasta superar el número
	// viejo (medido en producción, 25-09: 0 parciales en 120 s en demo-a).
	// Como un tramo dura ≥ 1 s de audio, un segmentador posterior siempre
	// arranca por encima de donde llegó el anterior.
	return &Segmentador{cfg: cfg, emitir: emitir, origen: origen, piso: -60, Nivel: -90, seq: origen.Unix()}
}

func (s *Segmentador) frames(d time.Duration) int {
	return int(d / (msFrame * time.Millisecond))
}

func (s *Segmentador) instante(muestra int64) time.Time {
	return s.origen.Add(time.Duration(muestra) * time.Second / FrecMuestreo)
}

// Escribir recibe PCM de cualquier largo y lo procesa por frames de 30 ms.
func (s *Segmentador) Escribir(pcm []int16) {
	for len(pcm) > 0 {
		falta := muestrasFrame - len(s.frame)
		n := min(falta, len(pcm))
		s.frame = append(s.frame, pcm[:n]...)
		pcm = pcm[n:]
		if len(s.frame) == muestrasFrame {
			s.procesarFrame(s.frame)
			s.frame = s.frame[:0]
		}
	}
}

func energiaDB(frame []int16) float64 {
	var suma float64
	for _, m := range frame {
		v := float64(m) / 32768
		suma += v * v
	}
	rms := math.Sqrt(suma / float64(len(frame)))
	if rms < 1e-5 {
		return -100
	}
	return 20 * math.Log10(rms)
}

func (s *Segmentador) actualizarPiso(e float64) {
	if !s.pisoOK {
		s.piso, s.pisoOK = e, true
		return
	}
	// Baja rápido y sube despacio: el piso sigue al ruido de la sala sin
	// que una frase larga lo arrastre hacia arriba y la voz deje de verse.
	if e < s.piso {
		s.piso = 0.8*s.piso + 0.2*e
	} else {
		s.piso = 0.995*s.piso + 0.005*e
	}
}

func (s *Segmentador) esVoz(e float64) bool {
	return e > s.cfg.PisoAbsoluto && e > s.piso+s.cfg.MargenDB
}

func (s *Segmentador) procesarFrame(frame []int16) {
	e := energiaDB(frame)
	s.Nivel = e
	voz := s.esVoz(e)
	s.actualizarPiso(e)
	inicioFrame := s.muestras
	s.muestras += int64(len(frame))

	if !s.abierto {
		if !voz {
			// Guardar un poco antes de la voz: el ataque de la primera
			// sílaba suele quedar debajo del umbral.
			s.prerrollo = append(s.prerrollo, frame...)
			if max := s.frames(s.cfg.PrerrolloVoz) * muestrasFrame; len(s.prerrollo) > max {
				s.prerrollo = s.prerrollo[len(s.prerrollo)-max:]
			}
			return
		}
		s.abierto = true
		s.buf = append(s.buf[:0], s.prerrollo...)
		s.energias = s.energias[:0]
		for i := 0; i < len(s.prerrollo)/muestrasFrame; i++ {
			s.energias = append(s.energias, s.cfg.PisoAbsoluto-1)
		}
		s.inicioBuf = inicioFrame - int64(len(s.prerrollo))
		s.ultParcial = s.inicioBuf
		s.prerrollo = s.prerrollo[:0]
		s.voz, s.silencio = 0, 0
	}

	s.buf = append(s.buf, frame...)
	s.energias = append(s.energias, e)
	if voz {
		s.voz++
		s.silencio = 0
	} else {
		s.silencio++
	}

	dur := time.Duration(len(s.buf)) * time.Second / FrecMuestreo
	switch {
	case dur >= s.cfg.Min && s.silencio >= s.frames(s.cfg.Pausa):
		s.cerrar(len(s.buf))
	case s.silencio >= s.frames(s.cfg.PausaLarga):
		s.cerrar(len(s.buf))
	case dur >= s.cfg.Max:
		s.cerrar(s.corteSilencioso())
	case s.cfg.CadaParcial > 0 && s.muestras-s.ultParcial >= int64(s.cfg.CadaParcial.Seconds()*FrecMuestreo):
		s.ultParcial = s.muestras
		if s.voz >= s.frames(s.cfg.VozMinima) {
			s.emitir(Tramo{PCM: append([]int16(nil), s.buf...), Inicio: s.instante(s.inicioBuf),
				Fin: s.instante(s.muestras), Seq: s.seq + 1, Final: false})
		}
	}
}

// corteSilencioso devuelve cuántas muestras de buf entran en el tramo: el
// final del frame más silencioso del último segundo y medio. Así el corte
// forzado cae entre palabras y no en medio de una.
func (s *Segmentador) corteSilencioso() int {
	ventana := s.frames(1500 * time.Millisecond)
	desde := max(len(s.energias)-ventana, 1)
	mejor, mejorE := len(s.energias)-1, math.Inf(1)
	for i := desde; i < len(s.energias); i++ {
		if s.energias[i] < mejorE {
			mejor, mejorE = i, s.energias[i]
		}
	}
	return (mejor + 1) * muestrasFrame
}

func (s *Segmentador) cerrar(n int) {
	tramo := append([]int16(nil), s.buf[:n]...)
	inicio := s.inicioBuf
	resto := append([]int16(nil), s.buf[n:]...)
	restoE := append([]float64(nil), s.energias[n/muestrasFrame:]...)

	// Tramos sin voz (aplausos cortos, un golpe, silencio) no se mandan:
	// Whisper rellena el vacío con frases inventadas, y además cuestan.
	if s.voz >= s.frames(s.cfg.VozMinima) {
		s.seq++
		s.emitir(Tramo{PCM: tramo, Inicio: s.instante(inicio), Fin: s.instante(inicio + int64(n)),
			Seq: s.seq, Final: true})
	}

	// Lo que quedó después del corte forzado sigue abierto como el
	// principio del próximo tramo.
	s.abierto = len(resto) > 0
	s.buf = append(s.buf[:0], resto...)
	s.energias = append(s.energias[:0], restoE...)
	s.inicioBuf = inicio + int64(n)
	s.ultParcial = s.inicioBuf
	s.voz, s.silencio = 0, 0
	for _, e := range restoE {
		if s.esVoz(e) {
			s.voz++
		}
	}
}

// Vaciar cierra lo que haya abierto: la sala dejó de emitir.
func (s *Segmentador) Vaciar() {
	if s.abierto && len(s.buf) > 0 {
		s.cerrar(len(s.buf))
	}
	s.abierto = false
	s.buf = s.buf[:0]
}
