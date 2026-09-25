import qrcode from "qrcode-generator";

// La matriz de un QR (nivel de corrección M: aguanta ~15 % de daño, que en
// un cartel impreso pegado en una puerta es lo normal).
export function matrizQR(texto: string): boolean[][] {
  const q = qrcode(0, "M");
  q.addData(texto);
  q.make();
  const n = q.getModuleCount();
  return Array.from({ length: n }, (_, y) => Array.from({ length: n }, (_, x) => q.isDark(y, x)));
}
