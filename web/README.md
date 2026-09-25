# conf-web — lo que ve la gente

Parte de [conf](https://github.com/MEMEMEMEMEMEMEDev/conf-hub): subtítulos y
traducción en vivo para conferencias. Astro estático con islas React, vestido
con la familia **pliego** de [`@ahroi/foundation`](https://github.com/MEMEMEMEMEMEMEDev/aegis-canvas).

| Ruta | Para quién |
|---|---|
| `/` | todos: las salas, qué es y lo medido |
| `/sala/?s=<sala>&l=es` | la audiencia: subtítulos en su idioma, letra grande, fondo oscuro; reacciones, «no se entiende», preguntas con votos y sugerir un nombre al glosario |
| `/s/<sala>` | la dirección corta del QR: redirige a la sala |
| `/cartel/?s=<sala>&l=es` | el proyector del escenario: nombre, estado, subtítulo en vivo y QR |
| `/obs/?s=<sala>&l=es&lineas=2&fondo=transparente` | OBS / vMix como *Browser Source* |
| `/emitir/?s=<sala>#<token>` | la consola de la sala: micrófono o audio de una pestaña |
| `/panel/` | producción: estado, backend, latencia, glosario, moderación de preguntas y sugerencias |

Todo le habla a su propio origen en `/api` (el hub). Los subtítulos llegan por
SSE y el navegador se reconecta solo con `Last-Event-ID`.

**Accesibilidad**: los subtítulos son un `role="log"` (el lector de pantalla
anuncia lo nuevo, no la región entera), la línea provisional no se anuncia, y
cada pantalla se recorre con Playwright a 390 px de ancho sin desborde ni
errores. Los componentes nuevos de pliego pasan axe sin violaciones.

```bash
npm ci
npm test          # vitest: el reductor de subtítulos (replay, parciales, idioma)
npm run check     # astro check
npm run build     # + guarda del artefacto: nombres con hash, sin restos de desarrollo
HUB_DEV=http://localhost:18080 npm run dev
```
