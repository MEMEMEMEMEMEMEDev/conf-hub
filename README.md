# conf — subtítulos y traducción en vivo para conferencias

Audio de cada sala en vivo → subtítulos en el idioma de la charla y su
traducción (inglés ↔ español), para personas sordas o con pérdida auditiva y
para quien no entiende el idioma del orador. Varias salas a la vez sobre **una
GPU de escritorio**, sin operadores y sin pagar por minuto. Open source
(Apache-2.0), desplegado y operado por **[aegis](https://github.com/MEMEMEMEMEMEMEDev/aegis-infra)**.

Hecho para la Nerdearla Vibeathon 2026.

> **English summary.** conf is an open-source live captioning and translation
> system for multi-room conferences. Each room streams 16 kHz PCM over a
> WebSocket; a pause-aware segmenter cuts 8–12 s segments; a single GPU process
> (faster-whisper large-v3-turbo + opus-mt on CTranslate2) serves every room and
> falls back to Gemini on Vertex AI by itself if the card fails. Captions reach
> phones, an OBS/vMix overlay and a production panel over Server-Sent Events,
> and export to SRT/VTT/TXT. Measured on an RTX 5070: 32 rooms at 0.44 s p95
> processing, ~0.0024 USD per room-hour in electricity.

## Qué pedían las bases y cómo lo resuelve

| Pedido | Cómo |
|---|---|
| Audio en vivo → subtítulos en tiempo real, original + español (y español → inglés) | Whisper transcribe en el idioma de la charla; opus-mt traduce en→es y es→en. |
| Varias sesiones al mismo tiempo | Un solo proceso en la GPU atiende todas las salas con el modelo compartido. Medido: 32 salas en una RTX 5070. |
| Open source y documentado | Apache-2.0. Tres repos: `conf-hub` (este), `conf-motor`, `conf-web`. |
| Gemini, o 100 % local | Local por defecto (Whisper + opus-mt, sin API ni cuota). Gemini 2.5 Flash-Lite como backend elegible por sala y como **respaldo automático** si la GPU falla. |
| Opcional: vista de audiencia | `/sala/?s=<sala>&l=es`: cada persona elige sala e idioma, tamaño de letra y fondo oscuro, desde el celular. |
| Opcional: exportar al final | `/api/salas/<sala>/export?formato=srt|vtt|txt&idioma=es`, cues partidos a dos renglones de 42. |
| Opcional: panel de producción | `/panel/`: por sala, estado, backend pedido y el que de verdad atendió, latencia p50/p95, tramos saltados, errores, audiencia. |
| Opcional: OBS / vMix | `/obs/?s=<sala>&l=es` como *Browser Source*: fondo transparente, letra grande con contorno. OBS lo compone sobre el stream. |
| Opcional: glosario | Por sala, editable en el panel; la audiencia puede sugerir un nombre mal escrito y producción lo aprueba. Whisper lo recibe como *hotwords*. |
| Más allá de las bases | Reacciones anónimas, «no se entiende» (una alarma de accesibilidad que ve producción), preguntas al orador con votos y moderación, y un cartel para el proyector del escenario con QR a `/s/<sala>`. |

## Probarlo en cinco minutos

1. Abrí la portada: hay dos **salas demo** sonando con fragmentos reales de
   charlas de Nerdearla 2025 (audio incluido en este repo, `demo/`). Entrá a una
   y los subtítulos aparecen en unos segundos.
2. Con tu propio audio: en `/panel/` creá una sala, abrí su enlace de emisión y
   elegí **micrófono**, **una pestaña** (por ejemplo, un video de YouTube de
   una charla) o **un archivo de audio**: se reproduce a ritmo real como si
   fuera en vivo.
3. Desde un stream de escenario, sin navegador:
   ```bash
   ffmpeg -re -i rtmp://… -f s16le -ar 16000 -ac 1 - | conf-emitir -url https://<dominio> -sala <sala> -token <token>
   ```

## Cómo funciona

```
navegador /emitir ──WebSocket PCM 16 kHz──┐                    ┌── /sala   (audiencia, SSE)
salas demo (audio grabado, en bucle) ─────┤                    ├── /obs    (overlay, SSE)
                                          ▼                    │
                          conf-hub (Go) · segmentador ─────────┤── /panel  (producción)
                                │  XADD conf:tramos             │   export SRT / VTT / TXT
                                ▼                               │
                              redis ◀── XADD conf:subs:<sala> ── conf-motor (Python, 1 proceso)
                                                                  large-v3-turbo fp16 + opus-mt
                                                                  ↳ Gemini (Vertex) por sala o de respaldo
```

- **Tramos de 8 a 12 s, cortados en las pausas.** Whisper procesa siempre una
  ventana de 30 s, así que un tramo corto no ahorra cómputo, y cortar cada 4 s
  parte palabras: el español pasó de 8,8 % a 20 % de WER. El segmentador mide
  energía en frames de 30 ms contra un piso de ruido que se adapta a la sala,
  cierra en una pausa de 300 ms y, si nadie respira, corta en el frame más
  silencioso del último segundo y medio. La idea es de
  [Subtitula](https://github.com/flordelcastillo/subtitula) (Apache 2.0); acá
  está reimplementada en Go con otros límites.
- **Llegar tarde es peor que perder una frase.** Un tramo con más de 25 s de
  atraso se salta, se cuenta y el panel lo muestra.
- **Si la GPU se cae, las salas siguen.** Un error de CUDA pasa todas las salas
  a Gemini solas; el panel dice «pide gpu, atiende gemini». Probado en la suite
  del motor con dobles; el simulacro sobre la máquina real se hace quitándole
  la GPU al motor antes de abrir la demo.
- **Nadie pierde una línea en un despliegue.** Cada subtítulo es una entrada de
  un stream de redis y su id es el `Last-Event-ID` del SSE: el navegador se
  reconecta solo y recibe lo que faltó, sin duplicados.
- **La audiencia participa sin texto libre salvo en las preguntas**, y esas
  nacen pendientes: sólo las ve quien la escribió y producción, hasta que se
  publican. Las reacciones se agregan una vez por segundo por sala. Todo lo
  que entra de la audiencia tiene un tope por visitante y por IP.
- **Salas demo** con fragmentos de charlas reales de Nerdearla 2025, marcadas
  como audio grabado en todas las vistas. El audio es grabado; la transcripción
  y la traducción se hacen en vivo cuando alguien abre la sala, y sólo entonces.

## Lo medido

Todo lo de esta sección lo midió el equipo experimental el 25-09-2026 en la
máquina donde corre la demo, con salas a ritmo real sobre dos fragmentos de
charlas de Nerdearla 2025. El método, los crudos y los guiones están en
[`conf-motor/mediciones/`](https://github.com/MEMEMEMEMEMEMEDev/conf-motor/tree/main/mediciones).

| Backend | Modelo | Salas a la vez | Proceso p95 | USD por sala-hora |
|---|---|---|---|---|
| **GPU RTX 5070 12 GB** | large-v3-turbo fp16 + opus-mt | **32** (40 no) | 0,44 s | **0,0024** ¹ |
| CPU, 8 núcleos físicos (Ryzen 9 5950X) | small int8 + opus-mt | 6 (8 no) | 6,3 s | hardware propio |
| Gemini 2.5 Flash-Lite (Vertex AI) | audio → texto y traducción en una llamada | cuota de la API | 2,9 s | 0,057 |
| Gemini 2.5 Flash (Vertex AI) | ídem | cuota de la API | 2,5 s | 0,228 |

¹ 180 W medidos en la GPU con 32 salas, más ~130 W estimados del resto del
equipo, a una tarifa **supuesta** de 0,25 USD/kWh. Sin amortizar el hardware.

| Calidad | Resultado |
|---|---|
| Transcripción, large-v3-turbo, tramos de 10 s | WER 8,5 % (inglés) · 8,8 % (español) |
| Lo mismo con tramos de 4 s | WER 9,2 % (inglés) · 20,2 % (español) |
| Traducción opus-mt sobre esa transcripción | chrF 54–61 |

**Lo que estos números NO dicen**, para que nadie los lea de más:

- La referencia de calidad es un borrador de Gemini 2.5 Flash, **no** una
  transcripción humana. Miden la distancia a ese borrador.
- La capacidad en GPU se midió con tramos de 4 s. conf usa tramos de 8 a 12 s,
  que son menos llamadas por sala: el techo medido es conservador para conf,
  pero conf no se midió a 32 salas tal cual corre.
- La demo pública corre con un tope de **28 salas** para dejar margen de memoria
  de video al escritorio de la máquina.
- En la prueba funcional de conf en esa misma GPU, un tramo de 10 s tardó
  0,17–0,21 s con una sala y el motor usó 3,1 GB de memoria de video. Es una
  sala, no una medición de carga.
- 2 charlas × 130 s es poco audio: los números de calidad son indicativos.

## Qué necesita

| | Qué | De dónde |
|---|---|---|
| GPU (camino principal) | una NVIDIA con 12 GB o más; probado en RTX 5070 (Blackwell) | — |
| Modelos | `whisper-large-v3-turbo` (CTranslate2), `opus-mt-en-es` y `opus-mt-es-en` convertidos a CTranslate2 int8; `whisper-small` para el respaldo en CPU | Hugging Face, sin credencial (ver `conf-motor/README.md`) |
| Gemini (opcional: respaldo o backend por sala) | una cuenta de servicio de Google Cloud con Vertex AI | variable `GOOGLE_APPLICATION_CREDENTIALS` en el motor |
| Sin GPU | funciona en CPU con `whisper-small`: ~6 salas en 8 núcleos, con más atraso | — |

No hace falta ninguna clave para probarlo en local con el motor en CPU.

## Escalar a más salas

La unidad de escala es la **sala**, y cada pieza escala distinto:

- **El motor** es un proceso por GPU que atiende todas las salas con el modelo
  compartido. Una RTX 5070 midió 32 salas. Cada GPU más es otro motor leyendo
  del mismo grupo de consumo de redis (`conf:tramos`): redis reparte los tramos
  entre los motores que haya, sin cambiar código. Es así por diseño; con dos
  GPU no se midió.
- **Sin GPU**, `BACKEND=cpu` corre `whisper-small`: medido, ~1,3 núcleos
  físicos por sala.
- **Gemini** no tiene techo propio: el límite es la cuota del proyecto de
  Google (medido: 0,057 USD por sala-hora con Flash-Lite). Una sala se pasa a
  Gemini desde el panel, en caliente.
- **El hub** no transcribe: recibe audio y reparte texto, con un solo lector
  de redis por sala que reparte en memoria a todos sus espectadores. Hoy es
  **una réplica**: guarda en memoria qué emisor está conectado a cada sala y
  las reacciones del último segundo. Para replicarlo, eso es lo primero que
  habría que pasar a redis; los subtítulos, las salas y las preguntas ya
  viven ahí.
- **Operación**: `MAX_SALAS` pone el techo del motor; el panel muestra por sala
  la latencia, los tramos saltados y quién atiende de verdad cada una.

## Desplegarlo

### Sobre aegis

conf es una organización de aegis. Su contrato declara cuatro servicios y la
plataforma deriva el resto (namespace, cuota, red, TLS, pipeline, dominio):

```yaml
organizacion: conf
dominio: conf.aaroidev.com
servicios:
  - {nombre: web,   tipo: estatico, publico: /,    repo: …/conf-web}
  - {nombre: hub,   tipo: http, puerto: 8080, publico: /api, repo: …/conf-hub, usa: [redis]}
  - {nombre: motor, tipo: worker, repo: …/conf-motor, usa: [redis, internet], gpu: true}
  - {nombre: bus,   tipo: redis}
```

Cada `git push` construye la imagen con los tests adentro (un test rojo y la
imagen no existe), la escanea, la firma y la despliega.

### Sin aegis

Son tres procesos y un redis. Variables del hub:

| Variable | Qué | Defecto |
|---|---|---|
| `REDIS_URL` | redis | `redis://localhost:6379/0` |
| `HUB_CLAVE_FIRMA` | firma de sesiones y tokens de emisión (≥ 16 caracteres, obligatoria) | — |
| `OPERADOR_USUARIO`, `OPERADOR_PASSWORD` | acceso al panel | — |
| `SALAS_DEMO` | salas demo en bucle | `2` |
| `DEMO_SOLO_CON_PUBLICO` | la demo manda audio al motor sólo si alguien mira | `1` |
| `TRAMO_MIN_S`, `TRAMO_MAX_S` | límites del segmentador | `8`, `12` |
| `BACKEND_DEFECTO` | backend de las salas nuevas | `gpu` |
| `PARCIAL_CADA_S` | cada cuánto se reenvía la frase abierta como parcial (`0` = sin parciales) | `0` |
| `CONFIAR_CF` | detrás de Cloudflare: usar `CF-Connecting-IP` como IP del visitante para el tope | `0` |

### En tu máquina, sin redis ni contenedores

```bash
# hub con un redis en memoria (sólo con el tag dev: operador/operador)
cd conf-hub && PORT=18080 REDIS_DEV_ADDR=127.0.0.1:16379 go run -tags dev .
# motor en CPU con whisper small (ver conf-motor/README.md)
cd conf-motor && REDIS_URL=redis://127.0.0.1:16379/0 BACKEND=cpu MODELOS=./modelos python -m motor
# front
cd conf-web && npm ci && HUB_DEV=http://localhost:18080 npm run dev
```

## Usarlo en un evento

1. En `/panel/` creá una sala por escenario (nombre, idioma de la charla,
   motor). Te da un enlace de emisión con el token de la sala después del `#`.
2. En la consola de sonido o en una notebook de la sala, abrí ese enlace y
   elegí micrófono o el audio de una pestaña (por ejemplo, el stream).
3. Para el stream: en OBS o vMix agregá un *Browser Source* con
   `https://<dominio>/obs/?s=<sala>&l=es` (1920×1080, fondo transparente).
4. Para la audiencia: en el proyector del escenario, `https://<dominio>/cartel/?s=<sala>`
   muestra el nombre de la sala, el subtítulo en vivo y un QR a `/s/<sala>`.
5. Al terminar, bajá el SRT o el VTT desde el panel. Renombrado como el video
   de la charla (`charla.mp4` → `charla.es.srt`), Plex, Jellyfin y merpy lo
   cargan solos como pista de subtítulos.

## Qué se hizo durante la Vibeathon y qué ya existía

conf —el hub, el motor, el front, el segmentador, las interacciones de sala— se
escribió el 24 y 25 de septiembre de 2026. Se apoya en dos herramientas del
mismo autor que ya existían, como se usa una librería:

- **aegis**, la plataforma GitOps que lo construye, firma, despliega y opera.
- **aegis-canvas**, el design system. Su familia *pliego* existía; los
  componentes de conf (subtítulos, cartel, QR, reacciones, preguntas, onda,
  tabla, estado, campo, cinta) se hicieron durante la Vibeathon y son
  públicos en ese repo.

## Créditos y licencias

- Código: Apache-2.0.
- Audio de las salas demo: fragmentos de charlas de Nerdearla 2025 incluidos en
  [flordelcastillo/subtitula](https://github.com/flordelcastillo/subtitula)
  (Apache 2.0), commit `edc5c89`.
- Modelos (no viajan en las imágenes): Whisper large-v3-turbo (MIT, OpenAI; la
  conversión a CTranslate2 es de mobiuslabsgmbh), opus-mt en-es y es-en de
  Helsinki-NLP (CC-BY 4.0).
- Design system: `@ahroi/foundation` (familia pliego), Apache-2.0.
