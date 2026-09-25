// Guarda sobre el ARTEFACTO, no sobre el código (guía 04 §3): lo que sale
// del build es lo que el borde va a servir, y hay errores que sólo se ven
// ahí.
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";

const dist = "dist";
const fallas = [];
const todos = (d) => readdirSync(d).flatMap((f) => (statSync(join(d, f)).isDirectory() ? todos(join(d, f)) : [join(d, f)]));
const archivos = todos(dist);

// 1. Las siete páginas existen.
for (const p of ["index.html", "sala/index.html", "obs/index.html", "emitir/index.html", "panel/index.html", "cartel/index.html", "orador/index.html"]) {
  if (!archivos.includes(join(dist, p))) fallas.push(`falta ${p}`);
}
// 2. El JS y el CSS llevan hash en el nombre: un cambio real es una URL
//    nueva y la caché del borde no puede servir una versión vieja
//    (guía 03, fallo 6: cuatro horas con Cloudflare sirviendo lo de antes).
for (const f of archivos.filter((f) => /\.(js|css)$/.test(f) && !f.endsWith("pcm-worklet.js"))) {
  if (!/[.-][A-Za-z0-9_-]{8,}\.(js|css)$/.test(f)) fallas.push(`sin hash en el nombre: ${f}`);
}
// 3. Ningún secreto ni dirección de desarrollo en lo que se publica.
for (const f of archivos.filter((f) => /\.(js|html|css)$/.test(f))) {
  const t = readFileSync(f, "utf8");
  if (/localhost:18080|operador\/operador|clave-de-desarrollo/.test(t)) fallas.push(`restos de desarrollo en ${f}`);
}
// 4. El worklet de audio viaja tal cual: /emitir lo carga por esa ruta.
if (!archivos.includes(join(dist, "pcm-worklet.js"))) fallas.push("falta pcm-worklet.js");

if (fallas.length) {
  console.error("guarda del artefacto:\n  " + fallas.join("\n  "));
  process.exit(1);
}
console.log(`guarda del artefacto: ${archivos.length} archivos, todo en orden`);
