// Los números de la portada. Copiados de mediciones/LEEME.md (experimental,
// 2026-09-25), que trae el método, los crudos y los guiones con que se
// midió cada uno. REGLA: nada entra acá sin estar allá. Un número sin cómo
// se midió no se publica (CLAUDE.md de conf, regla 4).

export const FECHA = "25-09-2026";

export const CAPACIDAD = [
  {
    backend: "GPU · RTX 5070 12 GB (casa)",
    modelo: "large-v3-turbo fp16 + opus-mt",
    salas: "32 (40 no)",
    proceso: "0,44 s",
    costo: "0,0024 USD*",
  },
  {
    backend: "CPU · 8 núcleos físicos (Ryzen 9 5950X)",
    modelo: "small int8 + opus-mt",
    salas: "6 (8 no)",
    proceso: "6,3 s",
    costo: "hardware propio",
  },
  {
    backend: "Gemini 2.5 Flash-Lite (Vertex AI)",
    modelo: "audio → texto y traducción, una llamada",
    salas: "cuota de la API",
    proceso: "2,9 s",
    costo: "0,057 USD",
  },
];

export const NOTAS_CAPACIDAD = [
  "Salas a ritmo real con dos fragmentos de charlas de Nerdearla 2025, en→es y es→en, en bucle. «Aguanta» = p95 de proceso ≤ 3 s y sin atraso acumulado (en CPU, sin atraso).",
  "La capacidad en GPU se midió con tramos de 4 s. conf usa tramos de 8 a 12 s cortados en las pausas: menos llamadas por sala, así que el techo medido es conservador. Esta demo corre con un tope de 28 salas para dejar margen de memoria de video.",
  "* Costo de la 5070: 180 W medidos en la GPU con 32 salas + ~130 W estimados del resto del equipo, a una tarifa SUPUESTA de 0,25 USD/kWh. Sin amortizar el hardware.",
];

export const CALIDAD = [
  { cosa: "Transcripción, large-v3-turbo, tramos de 10 s", valor: "WER 8,5 % (inglés) · 8,8 % (español)" },
  { cosa: "Lo mismo con tramos de 4 s", valor: "WER 9,2 % (inglés) · 20,2 % (español)" },
  { cosa: "Traducción opus-mt sobre la transcripción", valor: "chrF 54–61" },
];

export const NOTA_CALIDAD =
  "La referencia es un borrador de Gemini 2.5 Flash, NO una transcripción humana: estos números miden la distancia a ese borrador. Por eso conf corta en tramos de 8–12 s y no de 4 s.";
